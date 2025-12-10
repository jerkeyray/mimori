package raft

import (
	"context"
	"log"
	"time"

	raftpb "github.com/jerkeyray/mimori/internal/raft/raftpb"
)

func (r *Raft) RequestVote(ctx context.Context, req *raftpb.RequestVoteRequest) (*raftpb.RequestVoteResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	resp := &raftpb.RequestVoteResponse{
		Term: int32(r.term),
	}

	// 1. Reject request from a stale term
	if int(req.Term) < r.term {
		resp.VoteGranted = false
		return resp, nil
	}

	// 2. If the candidate has a newer term than us, update to it
	if int(req.Term) > r.term {
		r.term = int(req.Term)
		r.votedFor = ""
		r.state = Follower
		_ = r.meta.Save(r.term, r.votedFor)
	}

	// 3. Enforce Raft log up-to-date rule
	myLastIndex := len(r.log) - 1
	myLastTerm := r.log[myLastIndex].Term

	candidateLastTerm := int(req.LastLogTerm)
	candidateLastIndex := int(req.LastLogIndex)

	upToDate :=
		candidateLastTerm > myLastTerm ||
			(candidateLastTerm == myLastTerm && candidateLastIndex >= myLastIndex)

	if !upToDate {
		// Candidate's log is stale
		resp.VoteGranted = false
		return resp, nil
	}

	// 4. Check if we can vote for the candidate
	if r.votedFor == "" || r.votedFor == NodeID(req.CandidateId) {
		r.votedFor = NodeID(req.CandidateId)
		r.electionReset = time.Now()
		_ = r.meta.Save(r.term, r.votedFor)
		resp.VoteGranted = true
		return resp, nil
	}

	// 5. Already voted for someone else this term
	resp.VoteGranted = false
	return resp, nil
}

func (r *Raft) AppendEntries(ctx context.Context, req *raftpb.AppendEntriesRequest) (*raftpb.AppendEntriesResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	resp := &raftpb.AppendEntriesResponse{Term: int32(r.term)}

	// 1. Reject old term
	if int(req.Term) < r.term {
		resp.Success = false
		return resp, nil
	}

	// 2. Update to a newer term if needed
	if int(req.Term) > r.term {
		r.term = int(req.Term)
		r.votedFor = ""
		r.state = Follower
		_ = r.meta.Save(r.term, r.votedFor)
	}

	// 3. Set leader ID and heartbeat timestamp
	r.leader = NodeID(req.LeaderId)
	r.electionReset = time.Now()

	// 4. Log consistency check
	if req.PrevLogIndex > 0 {
		idx := int(req.PrevLogIndex)

		if idx >= len(r.log) || r.log[idx].Term != int(req.PrevLogTerm) {
			// follower's log doesn't match leader's
			resp.Success = false
			return resp, nil
		}
	}

	// 5. Append or overwrite entries
	for _, e := range req.Entries {
		idx := int(e.Index)

		if idx < len(r.log) {
			if r.log[idx].Term != int(e.Term) {
				// remove conflict
				r.log = r.log[:idx]
			} else {
				continue
			}
		}

		// append new entry
		r.log = append(r.log, LogEntry{
			Index: idx,
			Term:  int(e.Term),
			Data:  e.Data,
		})
	}

	// 6. Persist log
	if r.logStore != nil {
		if err := r.logStore.SaveAll(r.log); err != nil {
			log.Printf("[raft] follower failed to persist log: %v", err)
		}
	}

	// 7. Advance commit index
	if int(req.LeaderCommit) > r.commitIndex {
		last := len(r.log) - 1
		if int(req.LeaderCommit) < last {
			r.commitIndex = int(req.LeaderCommit)
		} else {
			r.commitIndex = last
		}
	}

	resp.Success = true
	return resp, nil
}
