package raft

import (
	"context"
	"io"
	"time"

	raftpb "github.com/jerkeyray/mimori/internal/raft/raftpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

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
				metricRPCErrorsTotal.WithLabelValues(string(r.id), "request_vote", "rpc_error").Inc()
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
		entries := make([]*raftpb.LogEntry, 0, lastIndex-prevLogIndex)
		for idx := nextIdx; idx <= lastIndex; idx++ {
			if e, ok := r.entryAt(idx); ok {
				entries = append(entries, &raftpb.LogEntry{
					Index: int32(e.Index),
					Term:  int32(e.Term),
					Data:  e.Data,
				})
			}
		}

		freshCommit := r.commitIndex
		r.mu.Unlock()

		go func(
			p NodeID,
			prevLogIndex int,
			prevLogTerm int,
			entries []*raftpb.LogEntry,
			freshCommit int,
		) {
			client, conn, err := r.dialPeer(string(p))
			if err != nil {
				return
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
				metricRPCErrorsTotal.WithLabelValues(string(leaderID), "append_entries", "rpc_error").Inc()
				return
			}

			// Process response under lock
			r.mu.Lock()
			defer r.mu.Unlock()

			// Discover higher term → step down
			if int(resp.Term) > r.term {
				r.term = int(resp.Term)
				r.state = Follower
				r.votedFor = ""
				_ = r.meta.Save(r.term, r.votedFor)
				return
			}

			// If follower rejected → backtrack or snapshot
			if !resp.Success {
				if r.snapshot != nil && r.nextIndex[p] <= r.snapshot.LastIncludedIndex {
					go r.sendSnapshotToFollower(p)
					return
				}
				if r.nextIndex[p] > r.logBaseIndex+1 {
					r.nextIndex[p]--
				}
				return
			}

			// Successful replication
			if len(entries) > 0 {
				lastSent := int(entries[len(entries)-1].Index)
				r.matchIndex[p] = lastSent
				r.nextIndex[p] = lastSent + 1
			} else {
				// Heartbeat success: follower matches up to prevLogIndex
				if prevLogIndex >= r.logBaseIndex {
					r.matchIndex[p] = prevLogIndex
					r.nextIndex[p] = prevLogIndex + 1
				}
			}

			// Try to advance leader commit index
			r.updateCommitIndexLocked()

		}(p, prevLogIndex, prevLogTerm, entries, freshCommit)
	}

	// Single-node cluster commits immediately
	if len(peers) == 0 {
		r.mu.Lock()
		r.updateCommitIndexLocked()
		r.mu.Unlock()
	}
}

func (r *Raft) dialPeer(addr string) (raftpb.RaftClient, io.Closer, error) {
	if r.dialer != nil {
		return r.dialer(addr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	conn, err := grpc.DialContext(
		ctx,
		addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)

	if err != nil {
		return nil, nil, err
	}

	return raftpb.NewRaftClient(conn), conn, nil
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

	start := time.Now()
	_, err = client.InstallSnapshot(ctx, &raftpb.InstallSnapshotRequest{
		Term:              int32(r.term),
		LeaderId:          string(r.id),
		LastIncludedIndex: int32(snap.LastIncludedIndex),
		LastIncludedTerm:  int32(snap.LastIncludedTerm),
		Data:              snap.Data,
	})
	duration := time.Since(start).Seconds()
	metricRPCInstallSnapshotDuration.WithLabelValues(string(r.id), string(p)).Observe(duration)

	if err != nil {
		metricRPCErrorsTotal.WithLabelValues(string(r.id), "install_snapshot", "rpc_error").Inc()
	}

	r.mu.Lock()
	r.matchIndex[p] = snap.LastIncludedIndex
	r.nextIndex[p] = snap.LastIncludedIndex + 1
	r.mu.Unlock()
}
