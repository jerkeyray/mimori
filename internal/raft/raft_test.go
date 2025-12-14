package raft

import (
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	raftpb "github.com/jerkeyray/mimori/internal/raft/raftpb"
	"google.golang.org/grpc"
)

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

	r.SetDialer(func(addr string) (raftpb.RaftClient, io.Closer, error) {
		n.mu.Lock()
		target, ok := n.nodes[addr]
		n.mu.Unlock()

		if !ok {
			return nil, nil, fmt.Errorf("node not found: %s", addr)
		}

		return &directClient{target: target}, io.Closer(&noOpCloser{}), nil
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

func (c *directClient) InstallSnapshot(ctx context.Context, in *raftpb.InstallSnapshotRequest, opts ...grpc.CallOption) (*raftpb.InstallSnapshotResponse, error) {
	return c.target.InstallSnapshot(ctx, in)
}

func (c *directClient) AddNode(ctx context.Context, in *raftpb.AddNodeRequest, opts ...grpc.CallOption) (*raftpb.AddNodeResponse, error) {
	return c.target.AddNode(ctx, in)
}

func (c *directClient) RemoveNode(ctx context.Context, in *raftpb.RemoveNodeRequest, opts ...grpc.CallOption) (*raftpb.RemoveNodeResponse, error) {
	return c.target.RemoveNode(ctx, in)
}

func (c *directClient) TimeoutNow(ctx context.Context, in *raftpb.TimeoutNowRequest, opts ...grpc.CallOption) (*raftpb.TimeoutNowResponse, error) {
	return c.target.TimeoutNow(ctx, in)
}

func (c *directClient) TransferLeadership(ctx context.Context, in *raftpb.TransferLeadershipRequest, opts ...grpc.CallOption) (*raftpb.TransferLeadershipResponse, error) {
	return c.target.TransferLeadership(ctx, in)
}

// noOpCloser is defined in rpc_client.go

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

func waitForLeader(t *testing.T, nodes ...*Raft) *Raft {
	t.Helper()
	timeout := time.After(5 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			t.Fatal("timed out waiting for leader election")
		case <-ticker.C:
			for _, n := range nodes {
				if n.IsLeader() {
					return n
				}
			}
		}
	}
}

func TestRaft_SnapshotCreation(t *testing.T) {
	dir := t.TempDir()
	id := NodeID("node-snap")
	r := New(id, []NodeID{}, dir)

	r.SetSnapshotter(func() ([]byte, error) {
		return []byte("snap-state"), nil
	})

	leader := waitForLeader(t, r)

	// Produce a handful of entries to apply
	for i := 0; i < 5; i++ {
		idx, err := leader.Propose([]byte(fmt.Sprintf("cmd-%d", i)))
		if err != nil {
			t.Fatalf("propose failed: %v", err)
		}
		select {
		case <-leader.AppliedWait(idx):
		case <-time.After(time.Second):
			t.Fatalf("apply wait timeout for %d", idx)
		}
	}

	leader.ForceSnapshot()

	if leader.snapshot == nil {
		t.Fatalf("snapshot not created")
	}

	if leader.logBaseIndex != leader.snapshot.LastIncludedIndex {
		t.Fatalf("logBaseIndex %d != snap index %d", leader.logBaseIndex, leader.snapshot.LastIncludedIndex)
	}

	// Next proposal should continue absolute indexing
	nextIdx, err := leader.Propose([]byte("post-snapshot"))
	if err != nil {
		t.Fatalf("propose after snapshot failed: %v", err)
	}
	expected := leader.snapshot.LastIncludedIndex + 1
	if nextIdx != expected {
		t.Fatalf("expected next index %d, got %d", expected, nextIdx)
	}

	select {
	case <-leader.AppliedWait(nextIdx):
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for post-snapshot apply")
	}

	if leader.lastApplied != nextIdx {
		t.Fatalf("lastApplied=%d expected=%d", leader.lastApplied, nextIdx)
	}
}

func TestRaft_InstallSnapshotToFollower(t *testing.T) {
	net := newMockNetwork()

	leaderID := NodeID("leader")
	followerID := NodeID("follower")

	leaderDir := t.TempDir()
	followerDir := t.TempDir()

	leader := New(leaderID, []NodeID{followerID}, leaderDir)
	follower := New(followerID, []NodeID{leaderID}, followerDir)

	net.register(leader)
	net.register(follower)

	restoreCh := make(chan []byte, 1)
	follower.SetSnapshotRestorer(func(data []byte) error {
		restoreCh <- data
		return nil
	})

	leader.SetSnapshotter(func() ([]byte, error) {
		return []byte("restorable-state"), nil
	})

	electedLeader := waitForLeader(t, leader, follower)
	if electedLeader != leader {
		t.Fatalf("expected %s to become leader, got %s", leaderID, electedLeader.id)
	}

	// Append some entries and apply on both nodes
	var lastIdx int
	for i := 0; i < 5; i++ {
		idx, err := leader.Propose([]byte(fmt.Sprintf("data-%d", i)))
		if err != nil {
			t.Fatalf("propose failed: %v", err)
		}
		lastIdx = idx
		select {
		case <-leader.AppliedWait(idx):
		case <-time.After(time.Second):
			t.Fatalf("timeout applying %d on leader", idx)
		}
	}

	// Ensure follower catches up with normal replication
	deadline := time.Now().Add(2 * time.Second)
	for {
		follower.mu.Lock()
		applied := follower.lastApplied
		follower.mu.Unlock()

		if applied >= lastIdx {
			break
		}

		if time.Now().After(deadline) {
			t.Fatalf("follower failed to replicate up to %d (got %d)", lastIdx, applied)
		}
		time.Sleep(25 * time.Millisecond)
	}

	// Force a snapshot on the leader
	leader.ForceSnapshot()
	if leader.snapshot == nil {
		t.Fatalf("leader snapshot missing")
	}

	// Make the leader think follower is far behind
	leader.mu.Lock()
	leader.nextIndex[followerID] = leader.snapshot.LastIncludedIndex
	leader.mu.Unlock()

	leader.sendHeartbeats()

	select {
	case data := <-restoreCh:
		if string(data) != "restorable-state" {
			t.Fatalf("restored data mismatch: %s", string(data))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("follower did not receive snapshot")
	}

	follower.mu.Lock()
	if follower.lastApplied != leader.snapshot.LastIncludedIndex {
		t.Fatalf("follower lastApplied=%d expected=%d", follower.lastApplied, leader.snapshot.LastIncludedIndex)
	}
	follower.mu.Unlock()
}
