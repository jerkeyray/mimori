package raft

import (
	"log"
	"sync"
	"time"
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
	electionTimeReset time.Time
}



