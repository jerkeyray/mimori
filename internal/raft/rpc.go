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

	// reject old terms
	if int(req.Term) < r.term {
		resp.Success = false
		return resp, nil
	}

	// newer term = respect leader
	if int(req.Term) > r.term {
		r.term = int(req.Term)
		r.votedFor = ""
		r.state = Follower
	}

	// heartbeat resets timer
	r.electionReset = time.Now()

	// log consistency check
	if req.PrevLogIndex > 0 {
		prevIdx := int(req.PrevLogIndex)

		if prevIdx >= len(r.log) ||
			r.log[prevIdx].Term != int(req.PrevLogTerm) {

			resp.Success = false
			return resp, nil
		}
	}

	// append entries
	for _, e := range req.Entries {
		idx := int(e.Index)

		// if entry exists but term mismatches → conflict
		if idx < len(r.log) {
			if r.log[idx].Term != int(e.Term) {
				r.log = r.log[:idx]
			} else {
				continue
			}
		}

		// append fresh entry
		r.log = append(r.log, LogEntry{
			Index: idx,
			Term:  int(e.Term),
			Data:  e.Data,
		})
	}

	// update commit index
	if int(req.LeaderCommit) > r.commitIndex {
		last := len(r.log) - 1
		r.commitIndex = min(int(req.LeaderCommit), last)
	}

	resp.Success = true
	return resp, nil
}
