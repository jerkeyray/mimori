package raft

import (
	"context"
	"time"

	"github.com/jerkeyray/mimori/internal/logging"
	raftpb "github.com/jerkeyray/mimori/internal/raft/raftpb"
)

func (r *Raft) RequestVote(ctx context.Context, req *raftpb.RequestVoteRequest) (*raftpb.RequestVoteResponse, error) {
	start := time.Now()
	defer func() {
		duration := time.Since(start).Seconds()
		metricRPCRequestVoteDuration.WithLabelValues(string(r.id)).Observe(duration)
	}()

	r.mu.Lock()
	defer r.mu.Unlock()

	// Ignore vote requests from nodes not in the current configuration.
	// This prevents term bumps and split-brain triggered by nodes that
	// haven't been added yet.
	if !r.isKnownNode(NodeID(req.CandidateId)) {
		return &raftpb.RequestVoteResponse{
			Term:        int32(r.term),
			VoteGranted: false,
		}, nil
	}

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
		_ = r.meta.Save(r.term, r.votedFor, r.peers)
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
		_ = r.meta.Save(r.term, r.votedFor, r.peers)
		resp.VoteGranted = true
		return resp, nil
	}

	// 5. Already voted for someone else this term
	resp.VoteGranted = false
	return resp, nil
}

func (r *Raft) AppendEntries(ctx context.Context, req *raftpb.AppendEntriesRequest) (*raftpb.AppendEntriesResponse, error) {
	start := time.Now()
	defer func() {
		duration := time.Since(start).Seconds()
		metricRPCAppendEntriesDuration.WithLabelValues(string(r.id)).Observe(duration)
	}()

	r.mu.Lock()
	defer r.mu.Unlock()

	// Ignore append entries from unknown leaders (not in current config).
	if !r.isKnownNode(NodeID(req.LeaderId)) {
		return &raftpb.AppendEntriesResponse{
			Term:    int32(r.term),
			Success: false,
		}, nil
	}

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
		_ = r.meta.Save(r.term, r.votedFor, r.peers)
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
			logging.WithRaftContext(string(r.id), r.term, r.state.String()).
				Err(err).Msg("follower failed to persist log")
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
		_ = r.meta.Save(r.term, r.votedFor, r.peers)
	}

	// 3. Install snapshot locally
	snap := &Snapshot{
		LastIncludedIndex: int(req.LastIncludedIndex),
		LastIncludedTerm:  int(req.LastIncludedTerm),
		CreatedAt:         time.Now().Unix(),
		Data:              req.Data,
	}

	if err := r.snapshotStore.Save(snap); err != nil {
		logging.WithRaftContext(string(r.id), r.term, r.state.String()).
			Err(err).Int("last_included_index", snap.LastIncludedIndex).
			Msg("snapshot save failed")
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
		logging.WithRaftContext(string(r.id), r.term, r.state.String()).
			Err(err).Msg("failed to persist log after snapshot install")
	}

	// Update progress indices
	r.commitIndex = snap.LastIncludedIndex
	r.lastApplied = snap.LastIncludedIndex

	if r.restorer != nil {
		if err := r.restorer(snap.Data); err != nil {
			logging.WithRaftContext(string(r.id), r.term, r.state.String()).
				Err(err).Int("last_included_index", snap.LastIncludedIndex).
				Msg("snapshot restore failed")
			// Note: metrics updated in ticker loop, not here
		}
	}

	metricSnapshotsInstalled.WithLabelValues(string(r.id)).Inc()
	return resp, nil
}

// AddNode handles the gRPC AddNode request
func (r *Raft) AddNode(ctx context.Context, req *raftpb.AddNodeRequest) (*raftpb.AddNodeResponse, error) {
	if !r.IsLeader() {
		return &raftpb.AddNodeResponse{
			Success: false,
			Error:   "not leader",
		}, nil
	}

	nodeID := NodeID(req.NodeId)
	if err := r.AddNodeInternal(nodeID); err != nil {
		return &raftpb.AddNodeResponse{
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	return &raftpb.AddNodeResponse{Success: true}, nil
}

// RemoveNode handles the gRPC RemoveNode request
func (r *Raft) RemoveNode(ctx context.Context, req *raftpb.RemoveNodeRequest) (*raftpb.RemoveNodeResponse, error) {
	if !r.IsLeader() {
		return &raftpb.RemoveNodeResponse{
			Success: false,
			Error:   "not leader",
		}, nil
	}

	nodeID := NodeID(req.NodeId)
	if err := r.RemoveNodeInternal(nodeID); err != nil {
		return &raftpb.RemoveNodeResponse{
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	return &raftpb.RemoveNodeResponse{Success: true}, nil
}

// TimeoutNow handles the TimeoutNow RPC request.
// This is used for leadership transfer - it causes the recipient to immediately
// start an election without waiting for the election timeout.
func (r *Raft) TimeoutNow(ctx context.Context, req *raftpb.TimeoutNowRequest) (*raftpb.TimeoutNowResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	resp := &raftpb.TimeoutNowResponse{
		Term:    int32(r.term),
		Success: false,
	}

	// Only accept from current leader
	if NodeID(req.LeaderId) != r.leader {
		return resp, nil
	}

	// Ignore if from a stale term
	if int(req.Term) < r.term {
		return resp, nil
	}

	// Update term if needed
	if int(req.Term) > r.term {
		r.term = int(req.Term)
		r.votedFor = ""
		r.state = Follower
		_ = r.meta.Save(r.term, r.votedFor, r.peers)
	}

	// Immediately start an election
	// This is safe because we know the leader sent this, meaning we're up-to-date
	r.startElectionLocked()

	logging.WithRaftContext(string(r.id), r.term, "candidate").
		Info().Str("triggered_by", req.LeaderId).
		Msg("timeout now received, starting election for leadership transfer")

	resp.Success = true
	resp.Term = int32(r.term)
	return resp, nil
}

// TransferLeadership handles the gRPC TransferLeadership request.
func (r *Raft) TransferLeadership(ctx context.Context, req *raftpb.TransferLeadershipRequest) (*raftpb.TransferLeadershipResponse, error) {
	if !r.IsLeader() {
		return &raftpb.TransferLeadershipResponse{
			Success: false,
			Error:   "not leader",
		}, nil
	}

	targetNodeID := NodeID(req.TargetNodeId)
	if err := r.TransferLeadershipInternal(targetNodeID); err != nil {
		return &raftpb.TransferLeadershipResponse{
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	return &raftpb.TransferLeadershipResponse{Success: true}, nil
}
