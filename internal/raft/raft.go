package raft

import (
	"errors"
	"io"
	"math/rand"
	"path"
	"path/filepath"
	"sync"
	"time"

	"github.com/jerkeyray/mimori/internal/logging"
	"github.com/jerkeyray/mimori/internal/raft/raftpb"
)

// RaftState represents what role a node is in
type RaftState int

const (
	Follower RaftState = iota
	Candidate
	Leader
)

// snapshotCheckThreshold controls when the leader will attempt a snapshot
// based on applied entries. Kept small for tests; tune for production loads.
const snapshotCheckThreshold = 50

type NodeID string

// Snapshot callbacks provided by KV layer
type Snapshotter func() ([]byte, error)
type SnapshotRestorer func([]byte) error

// Raft holds the consensus state
type Raft struct {
	raftpb.UnimplementedRaftServer
	mu sync.Mutex

	id    NodeID
	peers []NodeID

	state    RaftState
	term     int
	votes    int
	votedFor NodeID

	electionReset time.Time

	log         []LogEntry
	commitIndex int
	lastApplied int

	nextIndex  map[NodeID]int
	matchIndex map[NodeID]int

	applyCh chan LogEntry

	waiters map[int]chan struct{}

	leader NodeID

	meta     *MetaStore
	logStore *LogStore

	logBaseIndex int
	stopOnce     sync.Once

	dialer func(addr string) (raftpb.RaftClient, io.Closer, error)

	shutdownCh chan struct{}

	snapshotStore *SnapshotStore
	snapshot      *Snapshot

	snapshotter Snapshotter
	restorer    SnapshotRestorer
}

// -----------------------------------------------------------------------------
// Constructor
// -----------------------------------------------------------------------------

func New(id NodeID, peers []NodeID, dataDir string) *Raft {
	metaPath := path.Join(dataDir, "raft-meta.json")
	meta := NewMetaStore(metaPath)

	term, votedFor, _ := meta.Load()

	logPath := filepath.Join(dataDir, "raft-log.json")
	logStore := NewLogStore(logPath)

	entries, err := logStore.Load()
	if err != nil {
		logger := logging.With().Err(err).Str("component", "raft").Logger()
		logger.Error().Msg("failed to load log")
	}

	if len(entries) == 0 {
		entries = []LogEntry{{Index: 0, Term: 0, Data: nil}}
	}

	snapStore := NewSnapshotStore(filepath.Join(dataDir, "raft-snap.json"))
	snap, _ := snapStore.Load()

	// Align log base index with snapshot if present
	logBaseIndex := entries[0].Index
	if snap != nil {
		logBaseIndex = snap.LastIncludedIndex

		if len(entries) == 0 || entries[0].Index != snap.LastIncludedIndex {
			entries = append([]LogEntry{{
				Index: snap.LastIncludedIndex,
				Term:  snap.LastIncludedTerm,
				Data:  nil,
			}}, entries...)
		} else {
			entries[0].Index = snap.LastIncludedIndex
			entries[0].Term = snap.LastIncludedTerm
		}
	}

	r := &Raft{
		id:            id,
		peers:         peers,
		state:         Follower,
		term:          term,
		votedFor:      votedFor,
		electionReset: time.Now(),

		log: entries,

		nextIndex:    make(map[NodeID]int),
		matchIndex:   make(map[NodeID]int),
		applyCh:      make(chan LogEntry, 128),
		waiters:      make(map[int]chan struct{}),
		logBaseIndex: logBaseIndex,

		meta:          meta,
		logStore:      logStore,
		shutdownCh:    make(chan struct{}),
		snapshotStore: snapStore,
		snapshot:      snap,
	}

	for _, p := range peers {
		r.nextIndex[p] = r.lastLogIndex() + 1
		r.matchIndex[p] = 0
	}

	// If snapshot exists, Raft indexes must start from it
	if snap != nil {
		r.commitIndex = snap.LastIncludedIndex
		r.lastApplied = snap.LastIncludedIndex
	}

	go r.runElectionTimer()
	go r.runApplier()
	go r.runMetricsUpdater()

	return r
}

// -----------------------------------------------------------------------------
// Snapshot Callback Wiring
// -----------------------------------------------------------------------------

func (r *Raft) SetSnapshotter(s Snapshotter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snapshotter = s
}

func (r *Raft) SetSnapshotRestorer(rst SnapshotRestorer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.restorer = rst
}

// -----------------------------------------------------------------------------
// Log index helpers (handle compaction offsets)
// -----------------------------------------------------------------------------

func (r *Raft) lastLogIndex() int {
	if len(r.log) == 0 {
		return r.logBaseIndex
	}
	return r.log[len(r.log)-1].Index
}

func (r *Raft) lastLogTerm() int {
	if len(r.log) == 0 {
		return 0
	}
	return r.log[len(r.log)-1].Term
}

// entryAt returns the log entry with the given absolute index if present.
func (r *Raft) entryAt(idx int) (LogEntry, bool) {
	if len(r.log) == 0 {
		return LogEntry{}, false
	}
	if idx < r.logBaseIndex {
		return LogEntry{}, false
	}
	offset := idx - r.logBaseIndex
	if offset < 0 || offset >= len(r.log) {
		return LogEntry{}, false
	}
	return r.log[offset], true
}

// sliceFrom returns entries starting at absolute index `from` (inclusive).
func (r *Raft) sliceFrom(from int) []LogEntry {
	if from < r.logBaseIndex {
		from = r.logBaseIndex
	}
	start := from - r.logBaseIndex
	if start < 0 || start >= len(r.log) {
		return nil
	}
	out := make([]LogEntry, len(r.log)-start)
	copy(out, r.log[start:])
	return out
}

// -----------------------------------------------------------------------------
// Snapshot Creation (Leader Only)
// -----------------------------------------------------------------------------

func (r *Raft) createSnapshotLocked() {
	if r.snapshotter == nil {
		// app didn't register snapshot hook
		return
	}

	entry, ok := r.entryAt(r.lastApplied)
	if r.lastApplied < 0 || !ok {
		return
	}

	stateBytes, err := r.snapshotter()
	if err != nil {
		logging.WithRaftContext(string(r.id), r.term, r.state.String()).
			Err(err).Msg("snapshotter error")
		return
	}

	snap := &Snapshot{
		LastIncludedIndex: r.lastApplied,
		LastIncludedTerm:  entry.Term,
		CreatedAt:         time.Now().Unix(),
		Data:              stateBytes,
	}

	if err := r.snapshotStore.Save(snap); err != nil {
		logging.WithRaftContext(string(r.id), r.term, r.state.String()).
			Err(err).Int("last_included_index", snap.LastIncludedIndex).
			Msg("failed to save snapshot")
		return
	}

	r.snapshot = snap

	// Compact log — keep dummy entry at snapshot boundary
	newLog := []LogEntry{{
		Index: snap.LastIncludedIndex,
		Term:  snap.LastIncludedTerm,
		Data:  nil,
	}}

	for _, e := range r.log {
		if e.Index > r.lastApplied {
			newLog = append(newLog, e)
		}
	}

	r.log = newLog
	r.logBaseIndex = snap.LastIncludedIndex
	_ = r.logStore.SaveAll(r.log)

	metricSnapshotsCreated.WithLabelValues(string(r.id)).Inc()
}

// ForceSnapshot lets tests trigger a snapshot immediately.
func (r *Raft) ForceSnapshot() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.createSnapshotLocked()
}

// -----------------------------------------------------------------------------
// Query Helpers
// -----------------------------------------------------------------------------

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

func (r *Raft) ApplyCh() <-chan LogEntry { return r.applyCh }

// -----------------------------------------------------------------------------
// Election & Heartbeat Logic (unchanged from your base version)
// -----------------------------------------------------------------------------

func (r *Raft) randomElectionTimeout() time.Duration {
	return time.Duration(150+rand.Intn(150)) * time.Millisecond
}

func (r *Raft) runElectionTimer() {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	timeout := r.randomElectionTimeout()

	for {
		select {
		case <-r.shutdownCh:
			return
		case <-ticker.C:
		}

		r.mu.Lock()
		if r.state == Leader {
			r.mu.Unlock()
			continue
		}

		if time.Since(r.electionReset) >= timeout {
			r.startElectionLocked()
			timeout = r.randomElectionTimeout()
		}
		r.mu.Unlock()
	}
}

func (r *Raft) startElectionLocked() {
	r.state = Candidate
	r.term++
	r.votedFor = r.id
	r.meta.Save(r.term, r.votedFor)
	r.electionReset = time.Now()
	r.votes = 1

	if r.votes > len(r.peers)/2 {
		r.becomeLeaderLocked()
		return
	}

	go r.broadcastRequestVote(r.term)
	logging.WithRaftContext(string(r.id), r.term, "candidate").
		Info().Msg("starting election")
}

func (r *Raft) handleVoteResponse(resp *raftpb.RequestVoteResponse) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if int(resp.Term) > r.term {
		r.term = int(resp.Term)
		r.state = Follower
		r.votedFor = ""
		return
	}

	if r.state != Candidate {
		return
	}

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

	lastIdx := r.lastLogIndex() + 1
	for _, p := range r.peers {
		r.nextIndex[p] = lastIdx
		r.matchIndex[p] = r.logBaseIndex
	}

	go func() {
		t := time.NewTicker(75 * time.Millisecond)
		defer t.Stop()

		for {
			select {
			case <-r.shutdownCh:
				return
			case <-t.C:
			}

			r.mu.Lock()
			if r.state != Leader {
				r.mu.Unlock()
				return
			}
			r.mu.Unlock()

			r.sendHeartbeats()
		}
	}()
}

// -----------------------------------------------------------------------------
// Applier Loop
// -----------------------------------------------------------------------------

func (r *Raft) runApplier() {
	for {
		select {
		case <-r.shutdownCh:
			return
		default:
		}

		r.mu.Lock()
		for r.commitIndex > r.lastApplied {
			nextIndex := r.lastApplied + 1
			entry, ok := r.entryAt(nextIndex)
			if !ok {
				// we are likely waiting on a snapshot install; pause until available
				r.mu.Unlock()
				time.Sleep(5 * time.Millisecond)
				r.mu.Lock()
				continue
			}
			r.lastApplied = nextIndex
			r.mu.Unlock()

			r.applyCh <- entry
			metricAppliedTotal.WithLabelValues(string(r.id)).Inc()

			r.mu.Lock()
			if ch, ok := r.waiters[r.lastApplied]; ok {
				close(ch)
				delete(r.waiters, r.lastApplied)
			}

			// Try snapshotting after every apply
			if r.state == Leader && r.lastApplied-r.logBaseIndex >= snapshotCheckThreshold {
				r.createSnapshotLocked()
			}
		}
		r.mu.Unlock()

		time.Sleep(10 * time.Millisecond)
	}
}

// -----------------------------------------------------------------------------
// Client Proposal
// -----------------------------------------------------------------------------

var ErrNotLeader = errors.New("not leader")

func (r *Raft) Propose(cmdData []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.state != Leader {
		metricProposalsTotal.WithLabelValues(string(r.id), "not_leader").Inc()
		return 0, ErrNotLeader
	}

	index := r.lastLogIndex() + 1

	r.log = append(r.log, LogEntry{
		Index: index,
		Term:  r.term,
		Data:  cmdData,
	})

	if err := r.logStore.SaveAll(r.log); err != nil {
		logging.WithRaftContext(string(r.id), r.term, r.state.String()).
			Err(err).Msg("log persist failed")
	}

	metricProposalsTotal.WithLabelValues(string(r.id), "success").Inc()
	return index, nil
}

// -----------------------------------------------------------------------------
// Commit Advancement
// -----------------------------------------------------------------------------

func (r *Raft) updateCommitIndexLocked() {
	if r.state != Leader {
		return
	}

	total := len(r.peers) + 1
	majority := total/2 + 1

	for N := r.lastLogIndex(); N > r.commitIndex; N-- {
		entry, ok := r.entryAt(N)
		if !ok || entry.Term != r.term {
			continue
		}

		count := 1 // self
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

// -----------------------------------------------------------------------------
// Waiter
// -----------------------------------------------------------------------------

func (r *Raft) AppliedWait(index int) <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()

	if index <= r.lastApplied {
		ch := make(chan struct{})
		close(ch)
		return ch
	}

	ch := make(chan struct{}, 1)
	r.waiters[index] = ch
	return ch
}

// -----------------------------------------------------------------------------
// Status
// -----------------------------------------------------------------------------

func (r *Raft) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.updateMetrics() // Update metrics whenever status is queried

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

type Status struct {
	ID          string `json:"id"`
	State       string `json:"state"`
	Term        int    `json:"term"`
	LeaderID    string `json:"leader_id"`
	CommitIndex int    `json:"commit_index"`
	LastApplied int    `json:"last_applied"`
	LogLength   int    `json:"log_length"`
}

func (s RaftState) String() string {
	switch s {
	case Follower:
		return "follower"
	case Candidate:
		return "candidate"
	case Leader:
		return "leader"
	}
	return "unknown"
}

// -----------------------------------------------------------------------------
// Testing Helpers
// -----------------------------------------------------------------------------

func (r *Raft) SetDialer(d func(addr string) (raftpb.RaftClient, io.Closer, error)) {
	r.dialer = d
}

// runMetricsUpdater periodically updates Prometheus metrics
func (r *Raft) runMetricsUpdater() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.shutdownCh:
			return
		case <-ticker.C:
			r.mu.Lock()
			r.updateMetrics()
			r.mu.Unlock()
		}
	}
}

// Stop signals all internal goroutines to exit.
// Safe to call multiple times.
func (r *Raft) Stop() {
	r.stopOnce.Do(func() {
		close(r.shutdownCh)
	})
}
