package raft

import (
    "context"
    "time"

    raftpb "github.com/jerkeyray/mimori/internal/raft/raftpb"
    "google.golang.org/grpc"
    "google.golang.org/grpc/credentials/insecure"
)

// called when node becomes a candidate
func (r *Raft) broadcastRequestVote(term int) {
    for _, peer := range r.peers {
        peerID := peer

        if peerID == "" {
            continue
        }

        go func() {
            // give 300ms to create TCP connection and complete gRPC handshake
            ctxDial, cancelDial := context.WithTimeout(context.Background(), 300*time.Millisecond)
            conn, err := grpc.DialContext(ctxDial, string(peerID), grpc.WithTransportCredentials(insecure.NewCredentials()))
            cancelDial()
            if err != nil {
                return
            }
            defer conn.Close()

            client := raftpb.NewRaftClient(conn)

						// give 400ms for the RPC to run
            ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
            defer cancel()

            resp, err := client.RequestVote(ctx, &raftpb.RequestVoteRequest{
                CandidateId: string(r.id),
                Term:        int32(term),
            })
            if err != nil {
                return
            }

            // safe state update
            r.handleVoteResponse(resp)
        }()
    }
}

func (r *Raft) sendHeartbeats() {
    for _, peer := range r.peers {
        peerID := peer
        go func() {
            if peerID == "" {
                return
            }

            ctxDial, cancelDial := context.WithTimeout(context.Background(), 300*time.Millisecond)
            conn, err := grpc.DialContext(ctxDial, string(peerID), grpc.WithTransportCredentials(insecure.NewCredentials()))
            cancelDial()
            if err != nil {
                return
            }
            defer conn.Close()

            client := raftpb.NewRaftClient(conn)

            ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
            defer cancel()

            client.AppendEntries(ctx, &raftpb.AppendEntriesRequest{
                Term:     int32(r.term),
                LeaderId: string(r.id),
            })
        }()
    }
}
