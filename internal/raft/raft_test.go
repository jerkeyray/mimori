package raft

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	raftpb "github.com/jerkeyray/mimori/internal/raft/raftpb"
	"google.golang.org/grpc"
)

// SetDialer allows injecting a mock dialer for testing
func (r *Raft) SetDialer(d func(addr string) (raftpb.RaftClient, interface{ Close() error }, error)) {
	r.dialer = d
}

// Mock Network
type mockNetwork struct {
	mu    sync.Mutex
	nodes map[string]*Raft
}

func newMockNetwork() *mockNetwork {
	return &mockNetwork{
		nodes: make(map[string]*Raft),
	}
}

func (n *mockNetwork) register(r *Raft) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.nodes[string(r.id)] = r

	r.SetDialer(func(addr string) (raftpb.RaftClient, interface{ Close() error }, error) {
		n.mu.Lock()
		target, ok := n.nodes[addr]
		n.mu.Unlock()

		if !ok {
			return nil, nil, fmt.Errorf("node not found: %s", addr)
		}

		return &directClient{target: target}, &noOpCloser{}, nil
	})
}

type directClient struct {
	target *Raft
}

func (c *directClient) RequestVote(ctx context.Context, in *raftpb.RequestVoteRequest, opts ...grpc.CallOption) (*raftpb.RequestVoteResponse, error) {
	return c.target.RequestVote(ctx, in)
}

func (c *directClient) AppendEntries(ctx context.Context, in *raftpb.AppendEntriesRequest, opts ...grpc.CallOption) (*raftpb.AppendEntriesResponse, error) {
	return c.target.AppendEntries(ctx, in)
}

// noOpCloser is a closer that does nothing
type noOpCloser struct{}

func (c *noOpCloser) Close() error { return nil }

func TestRaft_SingleNode(t *testing.T) {
	dir := t.TempDir()
	id := NodeID("node1")
	r := New(id, []NodeID{}, dir)

	// Single node should become leader immediately upon election timeout
	// But in the code, election requires votes > len(peers)/2.
	// len(peers) is 0. 0/2 = 0.
	// r.votes = 1 (self). 1 > 0. So it should win.

	// Wait for leader
	timeout := time.After(2 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			t.Fatal("timed out waiting for leader election")
		case <-ticker.C:
			if r.IsLeader() {
				goto Elected
			}
		}
	}
Elected:

	if r.LeaderID() != id {
		t.Fatalf("expected leader %s, got %s", id, r.LeaderID())
	}

	// Test Propose
	idx, err := r.Propose([]byte("test-data"))
	if err != nil {
		t.Fatalf("propose failed: %v", err)
	}
	if idx != 1 {
		t.Fatalf("expected log index 1, got %d", idx)
	}

	// Wait for apply
	select {
	case <-r.AppliedWait(idx):
		// success
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for apply")
	}
}

func TestRaft_Replication(t *testing.T) {
	// Create cluster of 3 nodes
	net := newMockNetwork()
	peers := []NodeID{"node1", "node2", "node3"}
	nodes := make(map[NodeID]*Raft)
	dirs := make(map[NodeID]string)

	for _, id := range peers {
		dirs[id] = t.TempDir()
		// calculate peers for this node
		myPeers := make([]NodeID, 0)
		for _, p := range peers {
			if p != id {
				myPeers = append(myPeers, p)
			}
		}
		
		nodes[id] = New(id, myPeers, dirs[id])
		net.register(nodes[id])
	}

	// Wait for a leader to be elected
	var leader *Raft
	timeout := time.After(5 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

Loop:
	for {
		select {
		case <-timeout:
			t.Fatal("timed out waiting for leader election")
		case <-ticker.C:
			for _, n := range nodes {
				if n.IsLeader() {
					leader = n
					break Loop
				}
			}
		}
	}

	t.Logf("Leader elected: %s term %d", leader.id, leader.term)

	// Propose a command
	data := []byte("replication-test")
	idx, err := leader.Propose(data)
	if err != nil {
		t.Fatalf("propose failed: %v", err)
	}

	// Wait for apply on leader
	select {
	case <-leader.AppliedWait(idx):
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for leader apply")
	}

	// Wait for followers to apply
	start := time.Now()
	for {
		if time.Since(start) > 2*time.Second {
			t.Fatal("timed out waiting for followers to apply")
		}
		
		appliedCount := 0
		for _, n := range nodes {
			st := n.Status()
			if st.LastApplied >= idx {
				appliedCount++
			}
		}
		
		if appliedCount == len(peers) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
}
