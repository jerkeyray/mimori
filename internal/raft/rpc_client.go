package raft

import (
	"context"
	"io"
	"time"

	raftpb "github.com/jerkeyray/mimori/internal/raft/raftpb"
)

// noOpCloser is a no-op implementation of io.Closer.
// Used for pooled connections where closing is managed by the connection pool.
type noOpCloser struct{}

func (n *noOpCloser) Close() error {
	// No-op: connection is managed by the connection pool
	return nil
}

func (r *Raft) broadcastRequestVote(term int) {
	for _, peer := range r.peers {
		p := peer
		if p == "" {
			continue
		}

		go func(p NodeID) {
			// Snapshot last-log metadata under lock
			r.mu.Lock()
			lastIndex := r.lastLogIndex()
			lastTerm := r.lastLogTerm()
			r.mu.Unlock()

			client, conn, err := r.dialPeer(string(p))
			if err != nil {
				return
			}
			defer conn.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()

			resp, err := client.RequestVote(ctx, &raftpb.RequestVoteRequest{
				CandidateId:  string(r.id),
				Term:         int32(term),
				LastLogIndex: int32(lastIndex),
				LastLogTerm:  int32(lastTerm),
			})
			if err != nil {
				// Note: metrics updated in ticker loop, not here
				return
			}

			r.handleVoteResponse(resp)
		}(p)
	}
}

func (r *Raft) sendHeartbeats() {
	// Snapshot shared leader state
	r.mu.Lock()
	term := r.term
	leaderID := r.id
	peers := append([]NodeID(nil), r.peers...)
	r.mu.Unlock()

	for _, peer := range peers {
		p := peer
		if p == "" {
			continue
		}

		r.mu.Lock()
		nextIdx := r.nextIndex[p]
		if nextIdx == 0 {
			nextIdx = r.logBaseIndex + 1
		}
		snap := r.snapshot

		// follower is too far behind — send snapshot
		if snap != nil && nextIdx <= snap.LastIncludedIndex {
			r.mu.Unlock()
			go r.sendSnapshotToFollower(p)
			continue
		}

		prevLogIndex := nextIdx - 1
		prevLogTerm := 0
		if entry, ok := r.entryAt(prevLogIndex); ok {
			prevLogTerm = entry.Term
		}

		lastIndex := r.lastLogIndex()

		// Collect all pending entries
		allEntries := make([]*raftpb.LogEntry, 0, lastIndex-prevLogIndex)
		for idx := nextIdx; idx <= lastIndex; idx++ {
			if e, ok := r.entryAt(idx); ok {
				allEntries = append(allEntries, &raftpb.LogEntry{
					Index: int32(e.Index),
					Term:  int32(e.Term),
					Data:  e.Data,
				})
			}
		}

		freshCommit := r.commitIndex
		r.mu.Unlock()

		// Send entries in batches with pipelining
		go r.sendBatchesWithPipeline(p, term, leaderID, prevLogIndex, prevLogTerm, allEntries, freshCommit)
	}

	// Single-node cluster commits immediately
	if len(peers) == 0 {
		r.mu.Lock()
		r.updateCommitIndexLocked()
		r.mu.Unlock()
	}
}

// sendAppendEntriesBatch sends a single batch of entries to a follower.
// Returns true if the batch was successfully replicated, false otherwise.
func (r *Raft) sendAppendEntriesBatch(
	peer NodeID,
	term int,
	leaderID NodeID,
	prevLogIndex int,
	prevLogTerm int,
	entries []*raftpb.LogEntry,
	freshCommit int,
) bool {
	client, conn, err := r.dialPeer(string(peer))
	if err != nil {
		return false
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	resp, err := client.AppendEntries(ctx, &raftpb.AppendEntriesRequest{
		Term:         int32(term),
		LeaderId:     string(leaderID),
		PrevLogIndex: int32(prevLogIndex),
		PrevLogTerm:  int32(prevLogTerm),
		Entries:      entries,
		LeaderCommit: int32(freshCommit),
	})
	if err != nil {
		// Note: metrics updated in ticker loop, not here
		return false
	}

	// Process response under lock
	r.mu.Lock()
	defer r.mu.Unlock()

	// Discover higher term → step down
	if int(resp.Term) > r.term {
		r.term = int(resp.Term)
		r.state = Follower
		r.votedFor = ""
		_ = r.meta.Save(r.term, r.votedFor, r.peers)
		return false
	}

	// If follower rejected → backtrack or snapshot
	if !resp.Success {
		if r.snapshot != nil && r.nextIndex[peer] <= r.snapshot.LastIncludedIndex {
			go r.sendSnapshotToFollower(peer)
			return false
		}
		if r.nextIndex[peer] > r.logBaseIndex+1 {
			r.nextIndex[peer]--
		}
		return false
	}

	// Successful replication
	// Check if peer still exists (may have been removed while RPC was in-flight)
	peerExists := false
	for _, p := range r.peers {
		if p == peer {
			peerExists = true
			break
		}
	}
	if !peerExists {
		return false
	}

	if len(entries) > 0 {
		lastSent := int(entries[len(entries)-1].Index)
		r.matchIndex[peer] = lastSent
		r.nextIndex[peer] = lastSent + 1
	} else {
		// Heartbeat success: follower matches up to prevLogIndex
		if prevLogIndex >= r.logBaseIndex {
			r.matchIndex[peer] = prevLogIndex
			r.nextIndex[peer] = prevLogIndex + 1
		}
	}

	// Try to advance leader commit index
	r.updateCommitIndexLocked()

	return true
}

// sendBatchesWithPipeline sends log entries to a peer in batches with pipelining.
// Pipelining allows multiple AppendEntries RPCs to be in-flight concurrently,
// improving throughput especially for slow networks.
func (r *Raft) sendBatchesWithPipeline(
	peer NodeID,
	term int,
	leaderID NodeID,
	prevLogIndex int,
	prevLogTerm int,
	allEntries []*raftpb.LogEntry,
	freshCommit int,
) {
	// If no entries, send empty heartbeat
	if len(allEntries) == 0 {
		r.sendAppendEntriesBatchAsync(peer, term, leaderID, prevLogIndex, prevLogTerm, nil, freshCommit)
		return
	}

	// Get or create semaphore for this peer to limit concurrent requests
	sem := r.getPipelineSemaphore(peer)

	// Send entries in batches of maxEntriesPerBatch
	currentPrevIndex := prevLogIndex
	currentPrevTerm := prevLogTerm

	for i := 0; i < len(allEntries); i += maxEntriesPerBatch {
		end := i + maxEntriesPerBatch
		if end > len(allEntries) {
			end = len(allEntries)
		}

		batch := allEntries[i:end]

		// Acquire semaphore (limits concurrent in-flight RPCs)
		select {
		case sem <- struct{}{}:
			// Got permit, send batch asynchronously
		case <-r.shutdownCh:
			// Shutting down, stop sending
			return
		default:
			// Too many in-flight, wait a bit and retry
			time.Sleep(10 * time.Millisecond)
			continue
		}

		// Send batch asynchronously
		go func(b []*raftpb.LogEntry, prevIdx int, prevTrm int) {
			defer func() { <-sem }() // Release semaphore when done

			success := r.sendAppendEntriesBatch(peer, term, leaderID, prevIdx, prevTrm, b, freshCommit)

			if !success {
				// If batch failed, we'll retry on next heartbeat
				// Don't send remaining batches in this call
			}
		}(batch, currentPrevIndex, currentPrevTerm)

		// Update prevIndex and prevTerm for next batch
		if len(batch) > 0 {
			currentPrevIndex = int(batch[len(batch)-1].Index)
			currentPrevTerm = int(batch[len(batch)-1].Term)
		}

		// Small delay to allow first batch to start before sending next
		// This helps with pipelining without overwhelming the follower
		time.Sleep(5 * time.Millisecond)
	}
}

// sendAppendEntriesBatchAsync sends a batch asynchronously (non-blocking).
func (r *Raft) sendAppendEntriesBatchAsync(
	peer NodeID,
	term int,
	leaderID NodeID,
	prevLogIndex int,
	prevLogTerm int,
	entries []*raftpb.LogEntry,
	freshCommit int,
) {
	go func() {
		r.sendAppendEntriesBatch(peer, term, leaderID, prevLogIndex, prevLogTerm, entries, freshCommit)
	}()
}

// getPipelineSemaphore returns or creates a semaphore channel for limiting
// concurrent AppendEntries RPCs to a peer.
func (r *Raft) getPipelineSemaphore(peer NodeID) chan struct{} {
	r.pipeMu.Lock()
	defer r.pipeMu.Unlock()

	if sem, ok := r.pipelineSemaphores[peer]; ok {
		return sem
	}

	// Create new semaphore with capacity maxPipelineInflight
	sem := make(chan struct{}, maxPipelineInflight)
	r.pipelineSemaphores[peer] = sem
	return sem
}

// dialPeer returns a Raft client using the connection pool.
// The connection is cached and reused across RPC calls.
// Note: The returned io.Closer is a no-op for pooled connections
// (connections are managed by the pool). For custom dialers (e.g., tests),
// the dialer is called directly and its closer is returned.
func (r *Raft) dialPeer(addr string) (raftpb.RaftClient, io.Closer, error) {
	r.mu.Lock()
	hasCustomDialer := r.dialer != nil
	r.mu.Unlock()

	// For custom dialers (e.g., in tests), bypass the pool and use the dialer directly
	if hasCustomDialer {
		return r.dialer(addr)
	}

	// Use connection pool to get or create a cached connection
	client, err := r.connPool.getConnection(addr)
	if err != nil {
		return nil, nil, err
	}

	// For pooled connections, return a no-op closer since connections are managed by the pool
	return client, &noOpCloser{}, nil
}

func (r *Raft) sendSnapshotToFollower(p NodeID) {
	r.mu.Lock()
	snap := r.snapshot
	r.mu.Unlock()

	if snap == nil {
		var err error
		snap, err = r.snapshotStore.Load()
		if err != nil || snap == nil {
			return
		}
	}

	client, conn, err := r.dialPeer(string(p))
	if err != nil {
		return
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err = client.InstallSnapshot(ctx, &raftpb.InstallSnapshotRequest{
		Term:              int32(r.term),
		LeaderId:          string(r.id),
		LastIncludedIndex: int32(snap.LastIncludedIndex),
		LastIncludedTerm:  int32(snap.LastIncludedTerm),
		Data:              snap.Data,
	})
	if err != nil {
		// Note: metrics updated in ticker loop, not here
		// Log error but don't fail - snapshot send failures are handled gracefully
	}

	r.mu.Lock()
	r.matchIndex[p] = snap.LastIncludedIndex
	r.nextIndex[p] = snap.LastIncludedIndex + 1
	r.mu.Unlock()
}
