package raft

import (
    "context"
    "time"

    raftpb "github.com/jerkeyray/mimori/internal/raft/raftpb"
)

func (r *Raft) RequestVote(ctx context.Context, req *raftpb.RequestVoteRequest) (*raftpb.RequestVoteResponse, error) {
    r.mu.Lock()
    defer r.mu.Unlock()

    resp := &raftpb.RequestVoteResponse{Term: int32(r.term)}

		// if incoming term is less than local term, deny vote, return current term
    if int(req.Term) < r.term {
        resp.VoteGranted = false
        return resp, nil
    }

		// if incoming term is more than local term
		// update term, clear voted for, become follower
    if int(req.Term) > r.term {
        r.term = int(req.Term)
        r.votedFor = ""
        r.state = Follower
    }

		// if haven't voted already
		// grant vote and reset election timeout
    if r.votedFor == "" || r.votedFor == NodeID(req.CandidateId) {
        r.votedFor = NodeID(req.CandidateId)
        resp.VoteGranted = true
        r.electionReset = time.Now()
        return resp, nil
    }

    resp.VoteGranted = false
    return resp, nil
}

func (r *Raft) AppendEntries(ctx context.Context, req *raftpb.AppendEntriesRequest) (*raftpb.AppendEntriesResponse, error) {
    r.mu.Lock()
    defer r.mu.Unlock()

    resp := &raftpb.AppendEntriesResponse{Term: int32(r.term)}

		// if incoming term is less than local term, resp.Success = false
    if int(req.Term) < r.term {
        resp.Success = false
        return resp, nil
    }

		// else become follower 
    r.state = Follower
    r.term = int(req.Term)
    r.votedFor = NodeID(req.LeaderId)
    r.electionReset = time.Now()

    resp.Success = true
    return resp, nil
}
