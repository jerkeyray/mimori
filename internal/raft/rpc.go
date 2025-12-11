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
	myLastIndex := r.lastLogIndex()
	myLastTerm := r.lastLogTerm()

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
		if int(req.PrevLogIndex) < r.logBaseIndex {
			resp.Success = false
			return resp, nil
		}
		entry, ok := r.entryAt(int(req.PrevLogIndex))
		if !ok || entry.Term != int(req.PrevLogTerm) {
			// follower's log doesn't match leader's
			resp.Success = false
			return resp, nil
		}
	}

	// 5. Append or overwrite entries
	for _, e := range req.Entries {
		absIdx := int(e.Index)
		offset := absIdx - r.logBaseIndex

		// ignore entries older than our base snapshot
		if offset < 0 {
			continue
		}

		if offset < len(r.log) {
			if r.log[offset].Term != int(e.Term) {
				// remove conflict
				r.log = r.log[:offset]
			} else {
				continue
			}
		}

		// append new entry
		r.log = append(r.log, LogEntry{
			Index: absIdx,
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
		last := r.lastLogIndex()
		if int(req.LeaderCommit) < last {
			r.commitIndex = int(req.LeaderCommit)
		} else {
			r.commitIndex = last
		}
	}

	resp.Success = true
	return resp, nil
}

func (r *Raft) InstallSnapshot(ctx context.Context, req *raftpb.InstallSnapshotRequest) (*raftpb.InstallSnapshotResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	resp := &raftpb.InstallSnapshotResponse{Term: int32(r.term)}

	// 1. Reject stale terms
	if int(req.Term) < r.term {
		return resp, nil
	}

	// 2. If higher term → update term and convert to follower
	if int(req.Term) > r.term {
		r.term = int(req.Term)
		r.votedFor = ""
		r.state = Follower
		_ = r.meta.Save(r.term, r.votedFor)
	}

	// 3. Install snapshot locally
	snap := &Snapshot{
		LastIncludedIndex: int(req.LastIncludedIndex),
		LastIncludedTerm:  int(req.LastIncludedTerm),
		CreatedAt:         time.Now().Unix(),
		Data:              req.Data,
	}

	if err := r.snapshotStore.Save(snap); err != nil {
		log.Printf("[raft] snapshot save failed: %v", err)
	}

	r.snapshot = snap

	// Preserve entries that are beyond the snapshot index
	newLog := []LogEntry{{
		Index: snap.LastIncludedIndex,
		Term:  snap.LastIncludedTerm,
	}}
	for _, e := range r.log {
		if e.Index > snap.LastIncludedIndex {
			newLog = append(newLog, e)
		}
	}

	r.log = newLog
	r.logBaseIndex = snap.LastIncludedIndex

	if err := r.logStore.SaveAll(r.log); err != nil {
		log.Printf("[raft] failed to persist log after snapshot install: %v", err)
	}

	// Update progress indices
	r.commitIndex = snap.LastIncludedIndex
	r.lastApplied = snap.LastIncludedIndex

	if r.restorer != nil {
		if err := r.restorer(snap.Data); err != nil {
			log.Printf("[raft] snapshot restore failed: %v", err)
		}
	}

	return resp, nil
}
