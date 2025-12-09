package raft

import (
	"errors"
	"log"
	"math/rand"
	"path"
	"path/filepath"
	"sync"
	"time"

	raftpb "github.com/jerkeyray/mimori/internal/raft/raftpb"
)

// RaftState represents what role a node is in
type RaftState int

const (
	Follower RaftState = iota
	Candidate
	Leader
)

// node address
type NodeID string

// Raft holds the consensus state for a mimori node
type Raft struct {
	raftpb.UnimplementedRaftServer // REQUIRED for gRPC server interface
	mu                             sync.Mutex

	id    NodeID   // our address, e.g. ":4000"
	peers []NodeID // other nodes

	state    RaftState // follower, candidate, leader
	term     int       // current term
	votes    int
	votedFor NodeID // who we voted for

	// timers
	electionReset time.Time

	log         []LogEntry
	commitIndex int
	lastApplied int

	// for leaders: per follower progress
	nextIndex  map[NodeID]int
	matchIndex map[NodeID]int

	// channel to delvier commited entries to the state machine
	applyCh chan LogEntry

	waiters map[int]chan struct{} // waiting clients get notified when entry is applied

	leader NodeID

	meta     *MetaStore
	logStore *LogStore

	// injectable transport for testing
	dialer func(addr string) (raftpb.RaftClient, interface{ Close() error }, error)
}

// create a new Raft instance and start election timer in the background
func New(id NodeID, peers []NodeID, dataDir string) *Raft {
	metaPath := path.Join(dataDir, "raft-meta.json")
	meta := NewMetaStore(metaPath)

	term, votedFor, _ := meta.Load()

	// log file for raft log
	logPath := filepath.Join(dataDir, "raft-log.json")
	logStore := NewLogStore(logPath)

	entries, err := logStore.Load()
	if err != nil {
		log.Printf("[raft] failed to load log from disk: %v", err)
	}

	// always have dummy entry at index 0
	if len(entries) == 0 {
		entries = []LogEntry{
			{Index: 0, Term: 0, Data: nil},
		}
	}

	r := &Raft{
		id:            id,
		peers:         peers,
		state:         Follower,
		term:          term,
		votedFor:      votedFor,
		electionReset: time.Now(),
		log: []LogEntry{
			{Index: 0, Term: 0, Data: nil}, // dummy entry (Raft index starts at 1)
		},
		nextIndex:  make(map[NodeID]int),
		matchIndex: make(map[NodeID]int),
		applyCh:    make(chan LogEntry, 128),
		waiters:    make(map[int]chan struct{}),
		leader:     "",
		meta:       meta,
		logStore:   logStore,
	}

	for _, p := range peers {
		r.nextIndex[p] = 1
		r.matchIndex[p] = 0
	}

	go r.runElectionTimer()
	go r.runApplier()

	return r
}

func (r *Raft) randomElectionTimeout() time.Duration {
	// between 150ms and 300ms
	return time.Duration(150+rand.Intn(150)) * time.Millisecond
}

func (r *Raft) runElectionTimer() {
	// check every 50ms
	// if leader continue - no timeout
	// if no heartbeat heard in a while, start new election
	timeout := r.randomElectionTimeout()
	ticker := time.NewTicker(50 * time.Millisecond)

	for {
		<-ticker.C

		r.mu.Lock()
		if r.state == Leader {
			// leaders don't time out
			r.mu.Unlock()
			continue
		}

		// time since last heartbeat or vote
		if time.Since(r.electionReset) >= timeout {
			// become candidate
			r.startElectionLocked()
			timeout = r.randomElectionTimeout()
		}
		r.mu.Unlock()
	}
}

// node becomes a candidate and vote for yourself
func (r *Raft) startElectionLocked() {
	r.state = Candidate
	r.term++
	r.votedFor = r.id
	r.meta.Save(r.term, r.votedFor)
	r.electionReset = time.Now()
	r.votes = 1 // we vote for ourselves
	if r.votes > len(r.peers)/2 {
		r.becomeLeaderLocked()
		return
	}

	go r.broadcastRequestVote(r.term)

	log.Printf("[raft] %s starting election for term %d", r.id, r.term)
}

func (r *Raft) handleVoteResponse(resp *raftpb.RequestVoteResponse) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// if someone else has a higher term, revert to follower
	if int(resp.Term) > r.term {
		r.term = int(resp.Term)
		r.state = Follower
		r.votedFor = ""
		return
	}

	if r.state != Candidate {
		return
	}

	// if majority votes received, become leader
	if resp.VoteGranted {
		r.votes++
		if r.votes > len(r.peers)/2 {
			r.becomeLeaderLocked()
		}
	}
}

func (r *Raft) becomeLeaderLocked() {
	r.state = Leader
	r.leader = r.id
	log.Printf("[raft] %s became leader for term %d", r.id, r.term)

	// reset follower progress
	// on fresh leadership, assume followers might be behind
	lastIdx := len(r.log)
	for _, p := range r.peers {
		r.nextIndex[p] = lastIdx // send log starting from here
		r.matchIndex[p] = 0      // nothing confirmed yet
	}

	// become leader and start pulsing heartbeats every 75 ms
	go func() {
		ticker := time.NewTicker(75 * time.Millisecond)
		defer ticker.Stop()

		for {
			r.mu.Lock()
			if r.state != Leader {
				r.mu.Unlock()
				return
			}
			r.mu.Unlock()

			r.sendHeartbeats()
			<-ticker.C
		}
	}()
}

func (r *Raft) IsLeader() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state == Leader
}

func (r *Raft) LeaderID() NodeID {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.leader
}

func (r *Raft) ApplyCh() <-chan LogEntry {
	return r.applyCh
}

// push the commited log entries to the DB node
func (r *Raft) runApplier() {
	for {
		r.mu.Lock()
		for r.commitIndex > r.lastApplied {
			r.lastApplied++
			entry := r.log[r.lastApplied]
			r.mu.Unlock()

			// deliver to state machine
			r.applyCh <- entry
			r.mu.Lock()

			// notify waiters
			if ch, ok := r.waiters[r.lastApplied]; ok {
				close(ch)
				delete(r.waiters, r.lastApplied)
			}
		}
		r.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
}

var ErrNotLeader = errors.New("not leader")

func (r *Raft) Propose(cmdData []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.state != Leader {
		return 0, ErrNotLeader
	}

	// append a new uncommited log entry
	index := len(r.log)
	entry := LogEntry{
		Index: index,
		Term:  r.term,
		Data:  cmdData,
	}

	// append to in-memory log
	r.log = append(r.log, entry)

	// persist full log to disk
	if r.logStore != nil {
		if err := r.logStore.SaveAll(r.log); err != nil {
			log.Printf("[raft] failed to persist log: %v", err)
		}
	}

	return index, nil
}

// moves commitIndex forward if a majority
// of nodes have replicated a given index
func (r *Raft) updateCommitIndexLocked() {
	if r.state != Leader {
		return
	}

	// total nodes = followers + leader
	totalNodes := len(r.peers) + 1
	majority := (totalNodes / 2) + 1

	// walk from the end of the log down to current commitIndex
	for N := len(r.log) - 1; N > r.commitIndex; N-- {
		count := 1 // leader always has its own log

		// prevents committing old entries that could be overwritten
		if r.log[N].Term != r.term {
			continue
		}

		for _, p := range r.peers {
			if r.matchIndex[p] >= N {
				count++
			}
		}

		if count >= majority {
			r.commitIndex = N
			break
		}
	}
}

// returns channel that will be closed when the given
// log index has been applied to the state machine

func (r *Raft) AppliedWait(index int) <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()

	// if it's already applied, return closed channel
	if index <= r.lastApplied {
		ch := make(chan struct{})
		close(ch)
		return ch
	}

	ch := make(chan struct{}, 1)
	r.waiters[index] = ch
	return ch
}

// Status is a snapshot of the raft node state for debugging / observability.
type Status struct {
	ID          string `json:"id"`
	State       string `json:"state"`
	Term        int    `json:"term"`
	LeaderID    string `json:"leader_id"`
	CommitIndex int    `json:"commit_index"`
	LastApplied int    `json:"last_applied"`
	LogLength   int    `json:"log_length"`
}

// helper to turn RaftState into string
func (s RaftState) String() string {
	switch s {
	case Follower:
		return "follower"
	case Candidate:
		return "candidate"
	case Leader:
		return "leader"
	default:
		return "unknown"
	}
}

// Status returns a copy of the important raft state fields.
func (r *Raft) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()

	return Status{
		ID:          string(r.id),
		State:       r.state.String(),
		Term:        r.term,
		LeaderID:    string(r.leader),
		CommitIndex: r.commitIndex,
		LastApplied: r.lastApplied,
		LogLength:   len(r.log),
	}
}
