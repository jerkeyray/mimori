package raft

import (
	"log"
	"sync"
	"time"
	"math/rand"
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
	mu sync.Mutex

	id NodeID // our address, e.g. ":4000"
	peers []NodeID // other nodes
	state RaftState // follower, candidate, leader
	term int // current term
	votedFor NodeID // who we voted for

	// timers
	electionReset time.Time
}

// create a new Raft instance
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

// node becomes a candidate 
func (r *Raft) startElectionLocked() {
	r.state = Candidate
	r.term++
	r.votedFor = r.id
	r.electionReset = time.Now()
	
	log.Printf("[raft] %s starting election for term %d", r.id, r.term)
}