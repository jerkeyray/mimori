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

	// become a follower if the request term is newer
	if int(req.Term) > r.term {
		r.term = int(req.Term)
		r.state = Follower
		r.votedFor = ""
	}

	// reset election timeout
	r.electionReset = time.Now()

	// check log consistency
	if req.PrevLogIndex > 0 {
		if req.PrevLogIndex >= len(r.log) ||
			r.log[req.PrevLogIndex].Term != int(req.PrevLogTerm) {
			// follower log doesn’t match leader
			resp.Success = false
			return resp, nil
		}
	}

	// append any new entries after PrevLogIndex
	for _, entry := range req.Entries {
		if int(entry.Index) < len(r.log) {
			// if conflict, delete everything from that index onward
			if r.log[entry.Index].Term != int(entry.Term) {
				r.log = r.log[:entry.Index]
			} else {
				continue
			}
		}
		// append new entry
		r.log = append(r.log, LogEntry{
			Index: int(entry.Index),
			Term:  int(entry.Term),
			Data:  entry.Data,
		})
	}

	// update commit index
	if int(req.LeaderCommit) > r.commitIndex {
		r.commitIndex = min(int(req.LeaderCommit), len(r.log)-1)
	}

	resp.Success = true
	return resp, nil
}
