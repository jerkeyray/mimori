
package raft

import (
	"context"
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
			lastIndex := len(r.log) - 1
			lastTerm := r.log[lastIndex].Term
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

	logCopy := make([]LogEntry, len(r.log))
	copy(logCopy, r.log)

	peers := append([]NodeID(nil), r.peers...)
	r.mu.Unlock()

	for _, peer := range peers {
		p := peer
		if p == "" {
			continue
		}

		// Snapshot follower's nextIndex
		r.mu.Lock()
		nextIdx := r.nextIndex[p]
		if nextIdx < 1 {
			nextIdx = 1
		}
		r.mu.Unlock()

		// Compute prevLogIndex/Term from snapshot
		prevLogIndex := nextIdx - 1
		prevLogTerm := 0
		if prevLogIndex >= 0 && prevLogIndex < len(logCopy) {
			prevLogTerm = logCopy[prevLogIndex].Term
		}

		// Collect entries to send
		entries := make([]*raftpb.LogEntry, 0)
		for i := nextIdx; i < len(logCopy); i++ {
			e := logCopy[i]
			entries = append(entries, &raftpb.LogEntry{
				Index: int32(e.Index),
				Term:  int32(e.Term),
				Data:  e.Data,
			})
		}

		go func(
			p NodeID,
			prevLogIndex int,
			prevLogTerm int,
			entries []*raftpb.LogEntry,
		) {
			client, conn, err := r.dialPeer(string(p))
			if err != nil {
				return
			}
			defer conn.Close()

			// Fetch the FRESH commit index under lock
			r.mu.Lock()
			freshCommit := r.commitIndex
			r.mu.Unlock()

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

			// If follower rejected → backtrack nextIndex
			if !resp.Success {
				if r.nextIndex[p] > 1 {
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
				if prevLogIndex >= 0 {
					r.matchIndex[p] = prevLogIndex
				}
			}

			// Try to advance leader commit index
			r.updateCommitIndexLocked()

		}(p, prevLogIndex, prevLogTerm, entries)
	}

	// Single-node cluster commits immediately
	if len(peers) == 0 {
		r.mu.Lock()
		r.updateCommitIndexLocked()
		r.mu.Unlock()
	}
}

func (r *Raft) dialPeer(addr string) (raftpb.RaftClient, *grpc.ClientConn, error) {
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
