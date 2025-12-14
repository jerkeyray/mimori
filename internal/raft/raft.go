package raft

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand"
	"path"
	"path/filepath"
	"sync"
	"time"

	"github.com/jerkeyray/mimori/internal/logging"
	"github.com/jerkeyray/mimori/internal/raft/raftpb"
)

// RaftState represents what role a node is in
type RaftState int

const (
	Follower RaftState = iota
	Candidate
	Leader
)

// snapshotCheckThreshold controls when the leader will attempt a snapshot
// based on applied entries. Kept small for tests; tune for production loads.
const snapshotCheckThreshold = 50

type NodeID string

// Snapshot callbacks provided by KV layer
type Snapshotter func() ([]byte, error)
type SnapshotRestorer func([]byte) error

// Raft holds the consensus state
type Raft struct {
	raftpb.UnimplementedRaftServer
	mu sync.Mutex

	id    NodeID
	peers []NodeID

	state    RaftState
	term     int
	votes    int
	votedFor NodeID

	electionReset time.Time

	log         []LogEntry
	commitIndex int
	lastApplied int

	nextIndex  map[NodeID]int
	matchIndex map[NodeID]int

	applyCh chan LogEntry

	waiters map[int]chan struct{}

	leader NodeID

	meta     *MetaStore
	logStore *LogStore

	logBaseIndex int
	stopOnce     sync.Once

	dialer func(addr string) (raftpb.RaftClient, io.Closer, error)

	shutdownCh chan struct{}

	snapshotStore *SnapshotStore
	snapshot      *Snapshot

	snapshotter Snapshotter
	restorer    SnapshotRestorer
}

// -----------------------------------------------------------------------------
// Constructor
// -----------------------------------------------------------------------------

func New(id NodeID, peers []NodeID, dataDir string) *Raft {
	metaPath := path.Join(dataDir, "raft-meta.json")
	meta := NewMetaStore(metaPath)

	term, votedFor, savedPeers, _ := meta.Load()
	// Use saved peers if available (from previous config changes), otherwise use initial peers
	if len(savedPeers) > 0 {
		peers = savedPeers
	}

	logPath := filepath.Join(dataDir, "raft-log.json")
	logStore := NewLogStore(logPath)

	entries, err := logStore.Load()
	if err != nil {
		logger := logging.With().Err(err).Str("component", "raft").Logger()
		logger.Error().Msg("failed to load log")
	}

	if len(entries) == 0 {
		entries = []LogEntry{{Index: 0, Term: 0, Data: nil}}
	}

	snapStore := NewSnapshotStore(filepath.Join(dataDir, "raft-snap.json"))
	snap, _ := snapStore.Load()

	// Align log base index with snapshot if present
	logBaseIndex := entries[0].Index
	if snap != nil {
		logBaseIndex = snap.LastIncludedIndex

		if len(entries) == 0 || entries[0].Index != snap.LastIncludedIndex {
			entries = append([]LogEntry{{
				Index: snap.LastIncludedIndex,
				Term:  snap.LastIncludedTerm,
				Data:  nil,
			}}, entries...)
		} else {
			entries[0].Index = snap.LastIncludedIndex
			entries[0].Term = snap.LastIncludedTerm
		}
	}

	r := &Raft{
		id:            id,
		peers:         peers,
		state:         Follower,
		term:          term,
		votedFor:      votedFor,
		electionReset: time.Now(),

		log: entries,

		nextIndex:    make(map[NodeID]int),
		matchIndex:   make(map[NodeID]int),
		applyCh:      make(chan LogEntry, 128),
		waiters:      make(map[int]chan struct{}),
		logBaseIndex: logBaseIndex,

		meta:          meta,
		logStore:      logStore,
		shutdownCh:    make(chan struct{}),
		snapshotStore: snapStore,
		snapshot:      snap,
	}

	// Replay configuration changes from committed log entries
	// This ensures peers list matches the latest committed configuration
	r.replayConfigChanges()

	for _, p := range r.peers {
		r.nextIndex[p] = r.lastLogIndex() + 1
		r.matchIndex[p] = 0
	}

	// If snapshot exists, Raft indexes must start from it
	if snap != nil {
		r.commitIndex = snap.LastIncludedIndex
		r.lastApplied = snap.LastIncludedIndex
	}

	go r.runElectionTimer()
	go r.runApplier()
	go r.runMetricsUpdater()

	return r
}

// -----------------------------------------------------------------------------
// Snapshot Callback Wiring
// -----------------------------------------------------------------------------

func (r *Raft) SetSnapshotter(s Snapshotter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snapshotter = s
}

func (r *Raft) SetSnapshotRestorer(rst SnapshotRestorer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.restorer = rst
}

// -----------------------------------------------------------------------------
// Log index helpers (handle compaction offsets)
// -----------------------------------------------------------------------------

func (r *Raft) lastLogIndex() int {
	if len(r.log) == 0 {
		return r.logBaseIndex
	}
	return r.log[len(r.log)-1].Index
}

func (r *Raft) lastLogTerm() int {
	if len(r.log) == 0 {
		return 0
	}
	return r.log[len(r.log)-1].Term
}

// entryAt returns the log entry with the given absolute index if present.
func (r *Raft) entryAt(idx int) (LogEntry, bool) {
	if len(r.log) == 0 {
		return LogEntry{}, false
	}
	if idx < r.logBaseIndex {
		return LogEntry{}, false
	}
	offset := idx - r.logBaseIndex
	if offset < 0 || offset >= len(r.log) {
		return LogEntry{}, false
	}
	return r.log[offset], true
}

// sliceFrom returns entries starting at absolute index `from` (inclusive).
func (r *Raft) sliceFrom(from int) []LogEntry {
	if from < r.logBaseIndex {
		from = r.logBaseIndex
	}
	start := from - r.logBaseIndex
	if start < 0 || start >= len(r.log) {
		return nil
	}
	out := make([]LogEntry, len(r.log)-start)
	copy(out, r.log[start:])
	return out
}

// -----------------------------------------------------------------------------
// Snapshot Creation (Leader Only)
// -----------------------------------------------------------------------------

func (r *Raft) createSnapshotLocked() {
	if r.snapshotter == nil {
		// app didn't register snapshot hook
		return
	}

	entry, ok := r.entryAt(r.lastApplied)
	if r.lastApplied < 0 || !ok {
		return
	}

	stateBytes, err := r.snapshotter()
	if err != nil {
		logging.WithRaftContext(string(r.id), r.term, r.state.String()).
			Err(err).Msg("snapshotter error")
		return
	}

	snap := &Snapshot{
		LastIncludedIndex: r.lastApplied,
		LastIncludedTerm:  entry.Term,
		CreatedAt:         time.Now().Unix(),
		Data:              stateBytes,
	}

	if err := r.snapshotStore.Save(snap); err != nil {
		logging.WithRaftContext(string(r.id), r.term, r.state.String()).
			Err(err).Int("last_included_index", snap.LastIncludedIndex).
			Msg("failed to save snapshot")
		return
	}

	r.snapshot = snap

	// Compact log — keep dummy entry at snapshot boundary
	newLog := []LogEntry{{
		Index: snap.LastIncludedIndex,
		Term:  snap.LastIncludedTerm,
		Data:  nil,
	}}

	for _, e := range r.log {
		if e.Index > r.lastApplied {
			newLog = append(newLog, e)
		}
	}

	r.log = newLog
	r.logBaseIndex = snap.LastIncludedIndex
	_ = r.logStore.SaveAll(r.log)

	metricSnapshotsCreated.WithLabelValues(string(r.id)).Inc()
}

// ForceSnapshot lets tests trigger a snapshot immediately.
func (r *Raft) ForceSnapshot() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.createSnapshotLocked()
}

// -----------------------------------------------------------------------------
// Query Helpers
// -----------------------------------------------------------------------------

func (r *Raft) IsLeader() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state == Leader
}

func (r *Raft) LeaderID() NodeID {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.leader
}

func (r *Raft) ApplyCh() <-chan LogEntry { return r.applyCh }

// HasReadLease returns true if this node can safely serve reads.
// Leaders always have a read lease.
// Followers have a read lease if they've received a heartbeat recently
// (within the election timeout), indicating they're part of the active cluster.
func (r *Raft) HasReadLease() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Leaders can always serve reads
	if r.state == Leader {
		return true
	}

	// Followers can serve reads if they have a leader and received a heartbeat recently
	// The read lease is valid if we've received a heartbeat within the election timeout period.
	// This ensures we're still part of the active cluster.
	if r.state == Follower && r.leader != "" {
		// Check if last heartbeat was recent (within election timeout)
		// Use a conservative value: if we haven't received a heartbeat in 2x the typical
		// election timeout, we shouldn't serve reads (might be partitioned)
		maxLeaseAge := 300 * time.Millisecond // Conservative: 2x typical election timeout
		age := time.Since(r.electionReset)
		return age < maxLeaseAge
	}

	// Candidates or nodes without a leader cannot serve reads
	return false
}

// isKnownNode reports whether the given node ID is in the current configuration
// (self or peers). Used to ignore requests from unknown nodes to avoid term
// bumps / elections from nodes not yet added.
func (r *Raft) isKnownNode(id NodeID) bool {
	if id == "" {
		return false
	}
	if id == r.id {
		return true
	}
	for _, p := range r.peers {
		if p == id {
			return true
		}
	}
	return false
}

// -----------------------------------------------------------------------------
// Election & Heartbeat Logic (unchanged from your base version)
// -----------------------------------------------------------------------------

func (r *Raft) randomElectionTimeout() time.Duration {
	return time.Duration(150+rand.Intn(150)) * time.Millisecond
}

func (r *Raft) runElectionTimer() {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	timeout := r.randomElectionTimeout()

	for {
		select {
		case <-r.shutdownCh:
			return
		case <-ticker.C:
		}

		r.mu.Lock()
		if r.state == Leader {
			r.mu.Unlock()
			continue
		}

		if time.Since(r.electionReset) >= timeout {
			r.startElectionLocked()
			timeout = r.randomElectionTimeout()
		}
		r.mu.Unlock()
	}
}

func (r *Raft) startElectionLocked() {
	r.state = Candidate
	r.term++
	r.votedFor = r.id
	r.meta.Save(r.term, r.votedFor, r.peers)
	r.electionReset = time.Now()
	r.votes = 1

	// Majority is based on total voting members (self + peers).
	// With 2 nodes, majority=2, so self-vote alone is NOT enough.
	total := len(r.peers) + 1
	majority := total/2 + 1
	if r.votes >= majority {
		r.becomeLeaderLocked()
		return
	}

	go r.broadcastRequestVote(r.term)
	logging.WithRaftContext(string(r.id), r.term, "candidate").
		Info().Msg("starting election")
}

func (r *Raft) handleVoteResponse(resp *raftpb.RequestVoteResponse) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if int(resp.Term) > r.term {
		r.term = int(resp.Term)
		r.state = Follower
		r.votedFor = ""
		return
	}

	if r.state != Candidate {
		return
	}

	if resp.VoteGranted {
		r.votes++
		total := len(r.peers) + 1
		majority := total/2 + 1
		if r.votes >= majority {
			r.becomeLeaderLocked()
		}
	}
}

func (r *Raft) becomeLeaderLocked() {
	r.state = Leader
	r.leader = r.id

	lastIdx := r.lastLogIndex() + 1
	for _, p := range r.peers {
		r.nextIndex[p] = lastIdx
		r.matchIndex[p] = r.logBaseIndex
	}

	go func() {
		t := time.NewTicker(75 * time.Millisecond)
		defer t.Stop()

		for {
			select {
			case <-r.shutdownCh:
				return
			case <-t.C:
			}

			r.mu.Lock()
			if r.state != Leader {
				r.mu.Unlock()
				return
			}
			r.mu.Unlock()

			r.sendHeartbeats()
		}
	}()
}

// -----------------------------------------------------------------------------
// Applier Loop
// -----------------------------------------------------------------------------

func (r *Raft) runApplier() {
	for {
		select {
		case <-r.shutdownCh:
			return
		default:
		}

		r.mu.Lock()
		for r.commitIndex > r.lastApplied {
			nextIndex := r.lastApplied + 1
			entry, ok := r.entryAt(nextIndex)
			if !ok {
				// we are likely waiting on a snapshot install; pause until available
				r.mu.Unlock()
				time.Sleep(5 * time.Millisecond)
				r.mu.Lock()
				continue
			}
			r.lastApplied = nextIndex

			// Handle configuration changes internally before sending to applyCh
			r.handleConfigChangeIfNeeded(entry)

			r.mu.Unlock()

			r.applyCh <- entry
			metricAppliedTotal.WithLabelValues(string(r.id)).Inc()

			r.mu.Lock()
			if ch, ok := r.waiters[r.lastApplied]; ok {
				close(ch)
				delete(r.waiters, r.lastApplied)
			}

			// Try snapshotting after every apply
			if r.state == Leader && r.lastApplied-r.logBaseIndex >= snapshotCheckThreshold {
				r.createSnapshotLocked()
			}
		}
		r.mu.Unlock()

		time.Sleep(10 * time.Millisecond)
	}
}

// -----------------------------------------------------------------------------
// Client Proposal
// -----------------------------------------------------------------------------

var ErrNotLeader = errors.New("not leader")

func (r *Raft) Propose(cmdData []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.state != Leader {
		metricProposalsTotal.WithLabelValues(string(r.id), "not_leader").Inc()
		return 0, ErrNotLeader
	}

	index := r.lastLogIndex() + 1

	r.log = append(r.log, LogEntry{
		Index: index,
		Term:  r.term,
		Data:  cmdData,
	})

	if err := r.logStore.SaveAll(r.log); err != nil {
		logging.WithRaftContext(string(r.id), r.term, r.state.String()).
			Err(err).Msg("log persist failed")
	}

	metricProposalsTotal.WithLabelValues(string(r.id), "success").Inc()
	return index, nil
}

// -----------------------------------------------------------------------------
// Commit Advancement
// -----------------------------------------------------------------------------

func (r *Raft) updateCommitIndexLocked() {
	if r.state != Leader {
		return
	}

	total := len(r.peers) + 1
	majority := total/2 + 1

	for N := r.lastLogIndex(); N > r.commitIndex; N-- {
		entry, ok := r.entryAt(N)
		if !ok || entry.Term != r.term {
			continue
		}

		count := 1 // self
		for _, p := range r.peers {
			if r.matchIndex[p] >= N {
				count++
			}
		}

		if count >= majority {
			r.commitIndex = N
			break
		}
	}
}

// -----------------------------------------------------------------------------
// Waiter
// -----------------------------------------------------------------------------

func (r *Raft) AppliedWait(index int) <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()

	if index <= r.lastApplied {
		ch := make(chan struct{})
		close(ch)
		return ch
	}

	ch := make(chan struct{}, 1)
	r.waiters[index] = ch
	return ch
}

// -----------------------------------------------------------------------------
// Status
// -----------------------------------------------------------------------------

func (r *Raft) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()

	return Status{
		ID:          string(r.id),
		State:       r.state.String(),
		Term:        r.term,
		LeaderID:    string(r.leader),
		CommitIndex: r.commitIndex,
		LastApplied: r.lastApplied,
		LogLength:   len(r.log),
	}
}

type Status struct {
	ID          string `json:"id"`
	State       string `json:"state"`
	Term        int    `json:"term"`
	LeaderID    string `json:"leader_id"`
	CommitIndex int    `json:"commit_index"`
	LastApplied int    `json:"last_applied"`
	LogLength   int    `json:"log_length"`
}

func (s RaftState) String() string {
	switch s {
	case Follower:
		return "follower"
	case Candidate:
		return "candidate"
	case Leader:
		return "leader"
	}
	return "unknown"
}

// -----------------------------------------------------------------------------
// Configuration Management
// -----------------------------------------------------------------------------

// replayConfigChanges replays all committed configuration changes from the log
// to reconstruct the current peer list. Called during initialization.
func (r *Raft) replayConfigChanges() {
	// Start from logBaseIndex + 1 (first real entry after snapshot)
	startIdx := r.logBaseIndex + 1
	if startIdx < 1 {
		startIdx = 1
	}

	for idx := startIdx; idx <= r.lastLogIndex(); idx++ {
		entry, ok := r.entryAt(idx)
		if !ok {
			continue
		}

		// Check if this is a configuration change entry
		var cmd struct {
			Op     CommandType `json:"op"`
			Type   string      `json:"type,omitempty"`
			NodeID string      `json:"node_id,omitempty"`
		}
		if err := json.Unmarshal(entry.Data, &cmd); err != nil {
			continue
		}

		if cmd.Op == CmdConfigChange {
			r.applyConfigChangeLocked(cmd.Type == "add", NodeID(cmd.NodeID))
		}
	}
}

// applyConfigChangeLocked applies a configuration change to the peers list.
// Must be called with r.mu held.
func (r *Raft) applyConfigChangeLocked(isAdd bool, nodeID NodeID) {
	if nodeID == r.id {
		// Don't add/remove self
		return
	}

	if isAdd {
		// Add node if not already present
		found := false
		for _, p := range r.peers {
			if p == nodeID {
				found = true
				break
			}
		}
		if !found {
			r.peers = append(r.peers, nodeID)
			r.nextIndex[nodeID] = r.lastLogIndex() + 1
			r.matchIndex[nodeID] = 0
		}
	} else {
		// Remove node
		newPeers := make([]NodeID, 0, len(r.peers))
		for _, p := range r.peers {
			if p != nodeID {
				newPeers = append(newPeers, p)
			}
		}
		r.peers = newPeers
		delete(r.nextIndex, nodeID)
		delete(r.matchIndex, nodeID)
	}

	// Persist updated configuration
	_ = r.meta.Save(r.term, r.votedFor, r.peers)
}

// AddNodeInternal proposes adding a node to the cluster (internal API).
// Only the leader can propose configuration changes.
// Waits for the configuration change to be committed and applied before returning.
func (r *Raft) AddNodeInternal(nodeID NodeID) error {
	r.mu.Lock()
	index, err := r.proposeConfigChange(ConfigAddNode, nodeID)
	r.mu.Unlock()

	if err != nil {
		return err
	}

	// Kick replication / commit advancement (safe: sendHeartbeats acquires its own locks)
	// Important: proposeConfigChange must NOT call sendHeartbeats while holding r.mu.
	r.sendHeartbeats()

	// Wait for the configuration change to be committed and applied
	<-r.AppliedWait(index)
	return nil
}

// RemoveNodeInternal proposes removing a node from the cluster (internal API).
// Only the leader can propose configuration changes.
// Waits for the configuration change to be committed and applied before returning.
func (r *Raft) RemoveNodeInternal(nodeID NodeID) error {
	r.mu.Lock()
	index, err := r.proposeConfigChange(ConfigRemoveNode, nodeID)
	r.mu.Unlock()

	if err != nil {
		return err
	}

	// Kick replication / commit advancement (safe: sendHeartbeats acquires its own locks)
	// Important: proposeConfigChange must NOT call sendHeartbeats while holding r.mu.
	r.sendHeartbeats()

	// Wait for the configuration change to be committed and applied
	<-r.AppliedWait(index)
	return nil
}

// handleConfigChangeIfNeeded checks if an entry is a configuration change and applies it.
// Must be called with r.mu held.
func (r *Raft) handleConfigChangeIfNeeded(entry LogEntry) {
	var cmd struct {
		Op     CommandType `json:"op"`
		Type   string      `json:"type"`
		NodeID string      `json:"node_id"`
	}
	if err := json.Unmarshal(entry.Data, &cmd); err != nil {
		return
	}

	if cmd.Op == CmdConfigChange {
		isAdd := cmd.Type == "add"
		r.applyConfigChangeLocked(isAdd, NodeID(cmd.NodeID))

		logger := logging.WithRaftContext(string(r.id), r.term, r.state.String())
		logger.Info().
			Str("action", cmd.Type).
			Str("node_id", cmd.NodeID).
			Int("peer_count", len(r.peers)).
			Msg("configuration change applied")
	}
}

// proposeConfigChange proposes a configuration change log entry.
// Must be called with r.mu held. Returns the log index of the proposed entry.
func (r *Raft) proposeConfigChange(changeType ConfigChangeType, nodeID NodeID) (int, error) {
	if r.state != Leader {
		return 0, ErrNotLeader
	}

	// Don't allow removing the last node (would make cluster unavailable)
	if changeType == ConfigRemoveNode {
		if len(r.peers) <= 1 {
			return 0, errors.New("cannot remove last peer")
		}
		// Check if node exists
		found := false
		for _, p := range r.peers {
			if p == nodeID {
				found = true
				break
			}
		}
		if !found {
			return 0, errors.New("node not in cluster")
		}
	}

	// Wrap in command structure
	cmd := struct {
		Op     CommandType `json:"op"`
		Type   string      `json:"type"`
		NodeID string      `json:"node_id"`
	}{
		Op:     CmdConfigChange,
		Type:   map[ConfigChangeType]string{ConfigAddNode: "add", ConfigRemoveNode: "remove"}[changeType],
		NodeID: string(nodeID),
	}
	cmdData, err := json.Marshal(cmd)
	if err != nil {
		return 0, err
	}

	// Append to log
	index := r.lastLogIndex() + 1
	r.log = append(r.log, LogEntry{
		Index: index,
		Term:  r.term,
		Data:  cmdData,
	})

	if err := r.logStore.SaveAll(r.log); err != nil {
		logging.WithRaftContext(string(r.id), r.term, r.state.String()).
			Err(err).Msg("log persist failed")
		return 0, err
	}

	// Replicate to followers
	return index, nil
}

// -----------------------------------------------------------------------------
// Leader Transfer
// -----------------------------------------------------------------------------

// TransferLeadershipInternal initiates a leadership transfer to the target node.
// The leader will send a TimeoutNow RPC to the target, causing it to immediately
// start an election. Since the target is up-to-date (has been receiving heartbeats),
// it should win the election.
func (r *Raft) TransferLeadershipInternal(targetNodeID NodeID) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.state != Leader {
		return ErrNotLeader
	}

	// Can't transfer to self
	if targetNodeID == r.id {
		return errors.New("cannot transfer leadership to self")
	}

	// Check if target is in the cluster
	found := false
	for _, p := range r.peers {
		if p == targetNodeID {
			found = true
			break
		}
	}
	if !found {
		return errors.New("target node not in cluster")
	}

	// Ensure target is caught up (matchIndex should be at least commitIndex)
	if matchIdx, ok := r.matchIndex[targetNodeID]; ok {
		if matchIdx < r.commitIndex {
			return errors.New("target node is not caught up")
		}
	} else {
		return errors.New("target node not tracked")
	}

	// Send TimeoutNow RPC to target asynchronously
	go func() {
		client, conn, err := r.dialPeer(string(targetNodeID))
		if err != nil {
			logging.WithRaftContext(string(r.id), r.term, r.state.String()).
				Err(err).Str("target", string(targetNodeID)).
				Msg("failed to dial target for leadership transfer")
			return
		}
		defer conn.Close()

		r.mu.Lock()
		term := r.term
		leaderID := r.id
		r.mu.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		resp, err := client.TimeoutNow(ctx, &raftpb.TimeoutNowRequest{
			Term:     int32(term),
			LeaderId: string(leaderID),
		})

		if err != nil {
			logging.WithRaftContext(string(r.id), term, r.state.String()).
				Err(err).Str("target", string(targetNodeID)).
				Msg("timeout now RPC failed")
			return
		}

		// If target has a higher term, we've been superseded
		r.mu.Lock()
		if int(resp.Term) > r.term {
			r.term = int(resp.Term)
			r.state = Follower
			r.votedFor = ""
			r.leader = targetNodeID
			_ = r.meta.Save(r.term, r.votedFor, r.peers)
			logging.WithRaftContext(string(r.id), r.term, "follower").
				Info().Str("new_leader", string(targetNodeID)).
				Msg("leadership transferred successfully")
		}
		r.mu.Unlock()
	}()

	return nil
}

// -----------------------------------------------------------------------------
// Testing Helpers
// -----------------------------------------------------------------------------

func (r *Raft) SetDialer(d func(addr string) (raftpb.RaftClient, io.Closer, error)) {
	r.dialer = d
}

// runMetricsUpdater periodically updates Prometheus metrics.
// This is the ONLY place updateMetrics() is called.
func (r *Raft) runMetricsUpdater() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.shutdownCh:
			return
		case <-ticker.C:
			r.mu.Lock()
			r.updateMetrics()
			r.mu.Unlock()
		}
	}
}

// Stop signals all internal goroutines to exit.
// Safe to call multiple times.
func (r *Raft) Stop() {
	r.stopOnce.Do(func() {
		close(r.shutdownCh)
	})
}
