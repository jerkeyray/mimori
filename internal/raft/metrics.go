package raft

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// Node state: 1 if leader, 0 otherwise
	metricNodeIsLeader = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "raft_node_is_leader",
			Help: "Whether this node is the leader (1) or not (0)",
		},
		[]string{"node_id"},
	)

	// Current term
	metricTerm = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "raft_term",
			Help: "Current Raft term",
		},
		[]string{"node_id"},
	)

	// Commit index
	metricCommitIndex = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "raft_commit_index",
			Help: "Index of highest log entry known to be committed",
		},
		[]string{"node_id"},
	)

	// Last applied index
	metricLastAppliedIndex = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "raft_last_applied_index",
			Help: "Index of highest log entry applied to state machine",
		},
		[]string{"node_id"},
	)

	// Log length
	metricLogLength = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "raft_log_length",
			Help: "Number of entries in the Raft log",
		},
		[]string{"node_id"},
	)

	// Log base index (after compaction)
	metricLogBaseIndex = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "raft_log_base_index",
			Help: "Base index of the log (after compaction/snapshotting)",
		},
		[]string{"node_id"},
	)

	// Number of proposals
	metricProposalsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "raft_proposals_total",
			Help: "Total number of proposals made",
		},
		[]string{"node_id", "status"}, // status: "success" or "not_leader"
	)

	// Number of entries applied
	metricAppliedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "raft_applied_total",
			Help: "Total number of log entries applied to state machine",
		},
		[]string{"node_id"},
	)

	// Snapshot operations
	metricSnapshotsCreated = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "raft_snapshots_created_total",
			Help: "Total number of snapshots created",
		},
		[]string{"node_id"},
	)

	metricSnapshotsInstalled = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "raft_snapshots_installed_total",
			Help: "Total number of snapshots installed from leader",
		},
		[]string{"node_id"},
	)

	metricSnapshotSizeBytes = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "raft_snapshot_size_bytes",
			Help: "Size of the current snapshot in bytes",
		},
		[]string{"node_id"},
	)

	// RPC latencies (local node only, no peer labels)
	metricRPCRequestVoteDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "raft_rpc_request_vote_duration_seconds",
			Help:    "Duration of RequestVote RPC calls received by this node",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 10), // 1ms to ~1s
		},
		[]string{"node_id"},
	)

	metricRPCAppendEntriesDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "raft_rpc_append_entries_duration_seconds",
			Help:    "Duration of AppendEntries RPC calls received by this node",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 10),
		},
		[]string{"node_id"},
	)

	metricRPCInstallSnapshotDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "raft_rpc_install_snapshot_duration_seconds",
			Help:    "Duration of InstallSnapshot RPC calls received by this node",
			Buckets: prometheus.ExponentialBuckets(0.01, 2, 10), // 10ms to ~10s (snapshots are larger)
		},
		[]string{"node_id"},
	)

	// RPC errors
	metricRPCErrorsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "raft_rpc_errors_total",
			Help: "Total number of RPC errors",
		},
		[]string{"node_id", "rpc_type", "error_type"},
	)

	// Replication lag (for leader)
	metricReplicationLag = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "raft_replication_lag",
			Help: "Replication lag for each follower (commit_index - match_index)",
		},
		[]string{"node_id", "peer"},
	)
)

// updateMetrics updates all Prometheus metrics for THIS node only.
// Must be called with r.mu held. Only called from runMetricsUpdater ticker loop.
func (r *Raft) updateMetrics() {
	nodeID := string(r.id)

	// Node state
	if r.state == Leader {
		metricNodeIsLeader.WithLabelValues(nodeID).Set(1)
	} else {
		metricNodeIsLeader.WithLabelValues(nodeID).Set(0)
	}

	// Basic state
	metricTerm.WithLabelValues(nodeID).Set(float64(r.term))
	metricCommitIndex.WithLabelValues(nodeID).Set(float64(r.commitIndex))
	metricLastAppliedIndex.WithLabelValues(nodeID).Set(float64(r.lastApplied))
	metricLogLength.WithLabelValues(nodeID).Set(float64(len(r.log)))
	metricLogBaseIndex.WithLabelValues(nodeID).Set(float64(r.logBaseIndex))

	// Snapshot info
	if r.snapshot != nil {
		metricSnapshotSizeBytes.WithLabelValues(nodeID).Set(float64(len(r.snapshot.Data)))
	} else {
		metricSnapshotSizeBytes.WithLabelValues(nodeID).Set(0)
	}

	// Replication lag (leader only) - allowed to use peer label
	if r.state == Leader {
		for peer, matchIdx := range r.matchIndex {
			lag := r.commitIndex - matchIdx
			metricReplicationLag.WithLabelValues(nodeID, string(peer)).Set(float64(lag))
		}
	} else {
		// Clear replication lag metrics when not leader
		for peer := range r.matchIndex {
			metricReplicationLag.WithLabelValues(nodeID, string(peer)).Set(0)
		}
	}
}
