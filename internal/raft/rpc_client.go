package raft

import (
	"context"
	"time"

	raftpb "github.com/jerkeyray/mimori/internal/raft/raftpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// ask every peer for a vote (best effort RPC, timeout-protected)
func (r *Raft) broadcastRequestVote(term int) {
	for _, peer := range r.peers {
		p := peer // copy so goroutine doesn't race

		// if peer id is trash just skip
		if p == "" {
			continue
		}

		go func() {
			// open gRPC conn with small timeout
			client, conn, err := r.dialPeer(string(p))
			if err != nil {
				return // peer dead or slow, ignore
			}
			defer conn.Close()

			// send vote request
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()

			resp, err := client.RequestVote(ctx, &raftpb.RequestVoteRequest{
				CandidateId: string(r.id),
				Term:        int32(term),
				// NOTE: no log metadata yet, add it when log replication is wired
			})
			if err != nil {
				return
			}

			// update raft state using mutex-safe path
			r.handleVoteResponse(resp)
		}()
	}
}

// leader pings followers to keep them from starting elections
func (r *Raft) sendHeartbeats() {
	// snapshot shared state once so we don't hold lock across RPCs
	r.mu.Lock()
	term := r.term
	leaderID := r.id
	leaderCommit := r.commitIndex
	logCopy := make([]LogEntry, len(r.log))
	copy(logCopy, r.log)
	peers := append([]NodeID(nil), r.peers...)
	r.mu.Unlock()

	for _, peer := range peers {
		p := peer

		if p == "" {
			continue
		}

		go func() {
			client, conn, err := r.dialPeer(string(p))
			if err != nil {
				// peer is down or slow, ignore for now
				return
			}
			defer conn.Close()

			r.mu.Lock()
			// figure out what to send this follower
			nextIdx := r.nextIndex[p]
			if nextIdx < 1 {
				nextIdx = 1
			}

			prevLogIndex := nextIdx - 1
			prevLogTerm := 0
			if prevLogIndex >= 0 && prevLogIndex < len(logCopy) {
				prevLogTerm = logCopy[prevLogIndex].Term
			}

			// entries from nextIdx onward
			entries := make([]*raftpb.LogEntry, 0)
			for i := nextIdx; i < len(logCopy); i++ {
				e := logCopy[i]
				entries = append(entries, &raftpb.LogEntry{
					Index: int32(e.Index),
					Term:  int32(e.Term),
					Data:  e.Data,
				})
			}
			r.mu.Unlock()

			// build request
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancel()

			resp, err := client.AppendEntries(ctx, &raftpb.AppendEntriesRequest{
				Term:         int32(term),
				LeaderId:     string(leaderID),
				PrevLogIndex: int32(prevLogIndex),
				PrevLogTerm:  int32(prevLogTerm),
				Entries:      entries,
				LeaderCommit: int32(leaderCommit),
			})
			if err != nil {
				return
			}

			r.mu.Lock()
			defer r.mu.Unlock()

			// leader might be stale now
			if int(resp.Term) > r.term {
				r.term = int(resp.Term)
				r.state = Follower
				r.votedFor = ""
				return
			}

			// if follower rejected, back up nextIndex and try shorter prefix next time
			if !resp.Success {
				if r.nextIndex[p] > 1 {
					r.nextIndex[p]--
				}
				return
			}

			// success: follower now matches us up to last sent entry
			if len(entries) > 0 {
				lastSent := entries[len(entries)-1].Index
				r.matchIndex[p] = int(lastSent)
				r.nextIndex[p] = r.matchIndex[p] + 1
			}
		}()
		
		// leader checks if any new index can be committed
		r.mu.Lock()
		r.updateCommitIndexLocked()
		r.mu.Unlock()
	}

	// For single node clusters, we still need to check commit index
	if len(peers) == 0 {
		r.mu.Lock()
		r.updateCommitIndexLocked()
		r.mu.Unlock()
	}
}

// build client conn
func (r *Raft) dialPeer(addr string) (raftpb.RaftClient, interface{ Close() error }, error) {
	if r.dialer != nil {
		return r.dialer(addr)
	}
	// 250ms timeout is enough for vote RPCs
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
