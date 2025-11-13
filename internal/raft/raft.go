package raft

import (
	"log"
	"math/rand"
	"sync"
	"time"

	raftpb "github.com/jerkeyray/mimori/internal/raft/raftpb"
)

// RaftState represents what role a node is in
type RaftState int

const (
	Follower RaftState  = iota
	Candidate
	Leader
)

// node address
type NodeID string

// Raft holds the consensus state for a mimori node
type Raft struct {
	raftpb.UnimplementedRaftServer  // REQUIRED for gRPC server interface
	mu sync.Mutex

	id NodeID // our address, e.g. ":4000"
	peers []NodeID // other nodes
	state RaftState // follower, candidate, leader
	term int // current term
	votes int
	votedFor NodeID // who we voted for

	// timers
	electionReset time.Time
}

// create a new Raft instance and start election timer in the background
func New(id NodeID, peers []NodeID) *Raft {
	r := &Raft {
		id: id, 
		peers: peers,
		state: Follower,
		term: 0,
		votedFor: "",
		electionReset: time.Now(),
	}
	go r.runElectionTimer()
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
		<- ticker.C

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
	r.electionReset = time.Now()
	r.votes = 1 // we vote for ourselves

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
    log.Printf("[raft] %s became leader for term %d", r.id, r.term)

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
