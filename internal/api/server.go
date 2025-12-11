package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"

	"github.com/jerkeyray/mimori/internal/api/kv"
	"github.com/jerkeyray/mimori/internal/logging"
	"github.com/jerkeyray/mimori/internal/raft"
	"github.com/jerkeyray/mimori/internal/raft/raftpb"
	"github.com/jerkeyray/mimori/internal/storage"
	"github.com/jerkeyray/mimori/internal/utils"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RaftNode interface for mocking
type RaftNode interface {
	IsLeader() bool
	LeaderID() raft.NodeID
	Propose(cmdData []byte) (int, error)
	AppliedWait(index int) <-chan struct{}
	Status() raft.Status
}

// this file defines how our server responds to client commands(mimorictl)

// gRPC service implementation
type Server struct {
	kv.UnimplementedKVServer
	store storage.KV // pebble wrapper
	raft  RaftNode
}

func NewServer(store storage.KV, r RaftNode) *Server {
	return &Server{store: store, raft: r}
}

// gRPC method implementations

func (s *Server) Put(ctx context.Context, req *kv.PutRequest) (*kv.PutResponse, error) {
	// reject if follower
	if !s.raft.IsLeader() {
		return nil, status.Errorf(
			codes.FailedPrecondition,
			"not leader, leader=%s",
			s.raft.LeaderID(),
		)

	}

	data := encodePutCmd(req.Key, req.Value)

	// propose to raft log
	index, err := s.raft.Propose(data)
	if err != nil {
		return nil, err
	}

	// block until commited and applied
	<-s.raft.AppliedWait(index)

	return &kv.PutResponse{Ok: true}, nil
}

func (s *Server) Get(ctx context.Context, req *kv.GetRequest) (*kv.GetResponse, error) {
	// enforce leader reads for strong consistency
	if !s.raft.IsLeader() {
		return nil, status.Errorf(
			codes.FailedPrecondition,
			"not leader, leader=%s",
			s.raft.LeaderID(),
		)
	}

	val, found, err := s.store.Get(req.Key)
	if err != nil {
		return nil, err
	}
	return &kv.GetResponse{Value: val, Found: found}, nil
}

func (s *Server) Delete(ctx context.Context, req *kv.DeleteRequest) (*kv.DeleteResponse, error) {
	if !s.raft.IsLeader() {
		return nil, status.Errorf(
			codes.FailedPrecondition,
			"not leader, leader=%s",
			s.raft.LeaderID(),
		)
	}

	data := encodeDeleteCmd(req.Key)

	index, err := s.raft.Propose(data)
	if err != nil {
		return nil, err
	}

	// block until committed and applied
	<-s.raft.AppliedWait(index)

	return &kv.DeleteResponse{Deleted: true}, nil
}

func (s *Server) Health(ctx context.Context, _ *kv.HealthRequest) (*kv.HealthResponse, error) {
	return &kv.HealthResponse{Status: "ok"}, nil
}

// server launcher
func ListenAndServe(addr string, store storage.KV, raftNode *raft.Raft) error {
	// listen on the main gRPC address
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	// HTTP health endpoint
	// if node listens on :4000, HTTP health runs on :4001
	go func() {
		httpPort := parsePort(addr) + 1
		httpAddr := fmt.Sprintf(":%d", httpPort)

		mux := http.NewServeMux()

		// health check - basic liveness (server is running)
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
			st := raftNode.Status()
			// Health is OK if we can get status (node is responsive)
			// Even if not leader, node is healthy
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":     "ok",
				"node_id":    st.ID,
				"raft_state": st.State,
				"term":       st.Term,
			})
		})

		// readiness check - node is ready to serve requests
		mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
			st := raftNode.Status()
			ready := true
			reason := ""

			// Check if we have a leader (either us or someone else)
			if st.LeaderID == "" {
				ready = false
				reason = "no leader elected"
			}

			// Check if commit index is advancing (not stuck)
			// This is a simple check - in production you might want more sophisticated logic
			if st.CommitIndex == 0 && st.LastApplied == 0 {
				// First boot, might be OK
			}

			if ready {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"ready":      true,
					"node_id":    st.ID,
					"raft_state": st.State,
					"leader_id":  st.LeaderID,
				})
			} else {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"ready":      false,
					"reason":     reason,
					"node_id":    st.ID,
					"raft_state": st.State,
				})
			}
		})

		// raft status dump
		mux.HandleFunc("/raft/status", func(w http.ResponseWriter, r *http.Request) {
			st := raftNode.Status()

			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(st); err != nil {
				logger := logging.With().Err(err).Str("component", "api").Logger()
				logger.Error().Msg("status encode failed")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
		})

		// force snapshot creation (admin endpoint)
		mux.HandleFunc("/raft/snapshot", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}

			// Only leader can create snapshots
			if !raftNode.IsLeader() {
				w.WriteHeader(http.StatusPreconditionFailed)
				json.NewEncoder(w).Encode(map[string]string{
					"error":     "not leader",
					"leader_id": string(raftNode.LeaderID()),
				})
				return
			}

			// Force snapshot (raftNode is already *raft.Raft, no type assertion needed)
			raftNode.ForceSnapshot()
			st := raftNode.Status()

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":      "ok",
				"message":     "snapshot created",
				"raft_status": st,
			})
		})

		mux.Handle("/metrics", promhttp.Handler())

		logger := logging.With().Str("component", "api").Str("addr", httpAddr).Logger()
		logger.Info().Msg("HTTP endpoints started")
		if err := http.ListenAndServe(httpAddr, mux); err != nil {
			logger := logging.With().Err(err).Str("component", "api").Str("addr", httpAddr).Logger()
			logger.Error().Msg("HTTP server error")
		}
	}()

	// create gRPC server
	grpcServer := grpc.NewServer()

	// register KV service
	kv.RegisterKVServer(grpcServer, NewServer(store, raftNode))

	// register raft RPC service
	raftpb.RegisterRaftServer(grpcServer, raftNode)

	fmt.Printf("Mimori node listening on %s\n", addr)
	return grpcServer.Serve(lis)
}

// extracts port from string and returns the number
func parsePort(addr string) int {
	_, p := utils.ParseHostPort(addr)
	return p
}

type raftCommand struct {
	Op    raft.CommandType `json:"op"`
	Key   []byte           `json:"key"`
	Value []byte           `json:"value,omitempty"`
}

func encodePutCmd(key, val []byte) []byte {
	cmd := raftCommand{Op: raft.CmdPut, Key: key, Value: val}
	b, _ := json.Marshal(cmd)
	return b
}

func encodeDeleteCmd(key []byte) []byte {
	cmd := raftCommand{Op: raft.CmdDelete, Key: key}
	b, _ := json.Marshal(cmd)
	return b
}
