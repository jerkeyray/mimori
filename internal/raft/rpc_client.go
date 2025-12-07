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
	for _, peer := range r.peers {
		p := peer

		if p == "" {
			continue
		}

		go func() {
			client, conn, err := r.dialPeer(string(p))
			if err != nil {
				return // peer down, whatever
			}
			defer conn.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()

			// heartbeat = AppendEntries with zero entries
			client.AppendEntries(ctx, &raftpb.AppendEntriesRequest{
				Term:     int32(r.term),
				LeaderId: string(r.id),
				// no log fields yet; real Raft needs:
				// PrevLogIndex, PrevLogTerm, Entries, LeaderCommit
			})
		}()
	}
}


// build client conn
func (r *Raft) dialPeer(addr string) (raftpb.RaftClient, *grpc.ClientConn, error) {
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