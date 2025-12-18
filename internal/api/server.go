package api

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

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

//go:embed web/dashboard/*
var dashboardFS embed.FS

var dockerNodeIDRe = regexp.MustCompile(`^mimori-node(\d+):\d+$`)

// RaftNode interface for mocking
type RaftNode interface {
	IsLeader() bool
	LeaderID() raft.NodeID
	Propose(cmdData []byte) (int, error)
	AppliedWait(index int) <-chan struct{}
	Status() raft.Status
	HasReadLease() bool // Returns true if node has valid read lease (leader or follower with recent heartbeat)
	GetPeers() []raft.NodeID
	AddNodeInternal(nodeID raft.NodeID) error
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
	// Check if we can serve this read
	canServeRead := false

	if s.raft.IsLeader() {
		// Leader can always serve reads (strong consistency)
		canServeRead = true
	} else if req.AllowStale {
		// Follower can serve reads if:
		// 1. Client explicitly allows stale reads (AllowStale = true)
		// 2. Follower has a valid read lease (recent heartbeat from leader)
		if s.raft.HasReadLease() {
			canServeRead = true
		} else {
			// No valid lease - redirect to leader
			return nil, status.Errorf(
				codes.FailedPrecondition,
				"follower read lease expired, leader=%s",
				s.raft.LeaderID(),
			)
		}
	}

	if !canServeRead {
		// Default: require leader for strong consistency
		return nil, status.Errorf(
			codes.FailedPrecondition,
			"not leader, leader=%s (use allow_stale=true for follower reads)",
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

		// REST API endpoints for dashboard
		setupRESTAPI(mux, store, raftNode, addr)

		// Serve dashboard static files
		setupDashboard(mux)

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

// setupRESTAPI adds REST API endpoints for the dashboard
func setupRESTAPI(mux *http.ServeMux, store storage.KV, raftNode *raft.Raft, grpcAddr string) {
	api := &restAPIHandler{store: store, raft: raftNode}

	// KV operations
	mux.HandleFunc("/api/kv/", func(w http.ResponseWriter, r *http.Request) {
		// Extract key from path: /api/kv/{key}
		path := strings.TrimPrefix(r.URL.Path, "/api/kv/")
		if path == "" {
			http.Error(w, "key required", http.StatusBadRequest)
			return
		}
		key := []byte(path)

		switch r.Method {
		case http.MethodGet:
			api.handleGet(w, r, key)
		case http.MethodPut:
			api.handlePut(w, r, key)
		case http.MethodDelete:
			api.handleDelete(w, r, key)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Cluster operations
	mux.HandleFunc("/api/cluster/nodes", api.handleClusterNodes)
	mux.HandleFunc("/api/cluster/status", api.handleClusterStatus)
	mux.HandleFunc("/api/cluster/add-node", api.handleAddNode)
	mux.HandleFunc("/api/cluster/remove-node", api.handleRemoveNode)
	mux.HandleFunc("/api/cluster/transfer-leadership", api.handleTransferLeadership)
	mux.HandleFunc("/api/cluster/spawn-node", func(w http.ResponseWriter, r *http.Request) {
		handleSpawnNode(w, r, raftNode, grpcAddr)
	})

	// Enhanced status endpoint
	mux.HandleFunc("/api/status", api.handleStatus)
}

type restAPIHandler struct {
	store storage.KV
	raft  *raft.Raft
}

// spawnManager tracks spawned mimorid child processes
var spawner = newSpawnManager()

type spawnManager struct {
	mu    sync.Mutex
	next  int
	procs map[int]*exec.Cmd
}

func newSpawnManager() *spawnManager {
	return &spawnManager{
		next:  4002,
		procs: make(map[int]*exec.Cmd),
	}
}

func (m *spawnManager) nextFreePort(reserved []int) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	isReserved := func(p int) bool {
		for _, r := range reserved {
			if r == p {
				return true
			}
		}
		if _, ok := m.procs[p]; ok {
			return true
		}
		return false
	}

	for port := m.next; port < 10000; port += 2 {
		if isReserved(port) {
			continue
		}
		// Need BOTH gRPC port (port) and HTTP port (port+1) available.
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			continue
		}
		ln.Close()

		lnHTTP, err := net.Listen("tcp", fmt.Sprintf(":%d", port+1))
		if err != nil {
			continue
		}
		lnHTTP.Close()

		m.next = port + 2
		return port, nil
	}
	return 0, fmt.Errorf("no free port found")
}

func (m *spawnManager) track(port int, cmd *exec.Cmd) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.procs[port] = cmd
}

func (m *spawnManager) remove(port int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.procs, port)
}

func (m *spawnManager) get(port int) *exec.Cmd {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.procs[port]
}

func leaderHTTPBase(leaderID raft.NodeID) string {
	host, port := utils.ParseHostPort(string(leaderID))
	if host == "" {
		host = "localhost"
	}
	if port == 0 {
		// Best effort fallback: unknown port
		return ""
	}
	return fmt.Sprintf("http://%s:%d", host, port+1)
}

func publicHTTPBaseForNodeID(r *http.Request, nodeID raft.NodeID) string {
	// For browser-facing redirects/links we must return an address the browser can resolve.
	// In Docker Compose, node IDs look like "mimori-node2:4000" (dialable inside Docker),
	// but the browser must use published localhost ports (4001/4003/4005...).
	host, _, _ := net.SplitHostPort(r.Host)
	if host == "" {
		host = r.Host
	}
	if host == "" {
		host = "localhost"
	}

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if xf := r.Header.Get("X-Forwarded-Proto"); xf != "" {
		scheme = xf
	}

	id := string(nodeID)
	if m := dockerNodeIDRe.FindStringSubmatch(id); len(m) == 2 {
		n, _ := strconv.Atoi(m[1])
		if n > 0 {
			grpcHostPort := 4000 + 2*(n-1)
			httpHostPort := grpcHostPort + 1
			return fmt.Sprintf("%s://%s:%d", scheme, host, httpHostPort)
		}
	}

	// Local/manual mode: compute http port as grpcPort + 1 using the browser host.
	_, port := utils.ParseHostPort(id)
	if port == 0 {
		return ""
	}
	return fmt.Sprintf("%s://%s:%d", scheme, host, port+1)
}

func normalizeNodeIDForHost(nodeID raft.NodeID, host string) raft.NodeID {
	// If nodeID is missing a host (":4002"), fill it in ("localhost:4002") for consistency.
	h, p := utils.ParseHostPort(string(nodeID))
	if p == 0 {
		return nodeID
	}
	if h != "" {
		return nodeID
	}
	if host == "" {
		host = "localhost"
	}
	return raft.NodeID(fmt.Sprintf("%s:%d", host, p))
}

func requestHost(r *http.Request) string {
	if r == nil {
		return "localhost"
	}
	h, _, err := net.SplitHostPort(r.Host)
	if err == nil && h != "" {
		return h
	}
	if r.Host != "" {
		// If Host does not include a port, SplitHostPort fails; return it as-is.
		return r.Host
	}
	return "localhost"
}

func proxyToLeader(w http.ResponseWriter, r *http.Request, raftNode *raft.Raft) {
	leaderID := raftNode.LeaderID()
	if leaderID == "" {
		http.Error(w, "no leader elected", http.StatusServiceUnavailable)
		return
	}
	base := leaderHTTPBase(leaderID)
	if base == "" {
		http.Error(w, "leader http address unknown", http.StatusBadGateway)
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("read request body: %v", err), http.StatusBadRequest)
		return
	}
	_ = r.Body.Close()

	u := base + r.URL.Path
	if r.URL.RawQuery != "" {
		u += "?" + r.URL.RawQuery
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, u, bytes.NewReader(bodyBytes))
	if err != nil {
		http.Error(w, fmt.Sprintf("build proxy request: %v", err), http.StatusInternalServerError)
		return
	}

	// Preserve content type for JSON bodies.
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, fmt.Sprintf("proxy to leader failed: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func writeLeaderJSONError(w http.ResponseWriter, r *http.Request, code int, msg string, leaderID raft.NodeID) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error":       msg,
		"leader_id":   string(leaderID),
		"leader_http": publicHTTPBaseForNodeID(r, leaderID),
	})
}

func (h *restAPIHandler) handleGet(w http.ResponseWriter, r *http.Request, key []byte) {
	// Parse allow_stale query parameter
	allowStale := r.URL.Query().Get("allow_stale") == "true"

	// Strong reads should go to the leader. If we're a follower and the client did not
	// explicitly allow stale reads, forward to leader so the dashboard "Get" works from any node.
	if !h.raft.IsLeader() && !allowStale {
		proxyToLeader(w, r, h.raft)
		return
	}

	req := &kv.GetRequest{
		Key:        key,
		AllowStale: allowStale,
	}
	resp, err := NewServer(h.store, h.raft).Get(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"found": resp.Found,
		"value": string(resp.Value),
	})
}

func (h *restAPIHandler) handlePut(w http.ResponseWriter, r *http.Request, key []byte) {
	// Allow dashboard writes from any node: forward to leader if we are a follower.
	if !h.raft.IsLeader() {
		proxyToLeader(w, r, h.raft)
		return
	}

	var reqBody struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	req := &kv.PutRequest{
		Key:   key,
		Value: []byte(reqBody.Value),
	}
	_, err := NewServer(h.store, h.raft).Put(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok": true,
	})
}

func (h *restAPIHandler) handleDelete(w http.ResponseWriter, r *http.Request, key []byte) {
	// Allow dashboard deletes from any node: forward to leader if we are a follower.
	if !h.raft.IsLeader() {
		proxyToLeader(w, r, h.raft)
		return
	}

	req := &kv.DeleteRequest{Key: key}
	_, err := NewServer(h.store, h.raft).Delete(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"deleted": true,
	})
}

func (h *restAPIHandler) handleClusterNodes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	peers := h.raft.GetPeers()
	st := h.raft.Status()

	type apiStatus struct {
		NodeID   string `json:"node_id"`
		State    string `json:"state"`
		Term     int    `json:"term"`
		IsLeader bool   `json:"is_leader"`
	}

	type nodeView struct {
		ID        string `json:"id"`
		State     string `json:"state"`
		Term      int    `json:"term"`
		IsLeader  bool   `json:"is_leader"`
		Reachable bool   `json:"reachable"`
	}

	// Pre-create peer views so we always return a stable set of nodes
	// (no flicker due to goroutine completion order).
	peerViews := make([]nodeView, len(peers))
	for i, p := range peers {
		peerViews[i] = nodeView{
			ID:        string(p),
			State:     "unknown",
			Term:      0,
			IsLeader:  false,
			Reachable: false,
		}
	}

	// Be generous here: this endpoint is for UI display, not for correctness-critical ops.
	// Too-aggressive timeouts cause "unreachable" flicker when many local nodes are running.
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	var wg sync.WaitGroup

	for i := range peers {
		i := i
		peerID := peers[i]
		wg.Add(1)
		go func() {
			defer wg.Done()

			base := leaderHTTPBase(peerID) // nodeID -> http base (port+1)
			if base == "" {
				peerViews[i].State = "unknown"
				peerViews[i].Reachable = false
				return
			}

			tryOnce := func() (*apiStatus, int, error) {
				req, err := http.NewRequestWithContext(
					r.Context(),
					http.MethodGet,
					base+"/api/status?t="+fmt.Sprint(time.Now().UnixNano()),
					nil,
				)
				if err != nil {
					return nil, 0, err
				}

				resp, err := client.Do(req)
				if err != nil || resp == nil {
					return nil, 0, err
				}
				defer resp.Body.Close()

				if resp.StatusCode != http.StatusOK {
					return nil, resp.StatusCode, fmt.Errorf("http %d", resp.StatusCode)
				}

				var s apiStatus
				if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
					return nil, resp.StatusCode, err
				}
				return &s, resp.StatusCode, nil
			}

			s, _, err := tryOnce()
			if err != nil {
				// One quick retry to reduce UI flicker.
				time.Sleep(50 * time.Millisecond)
				s, _, err = tryOnce()
				if err != nil {
					peerViews[i].State = "unreachable"
					peerViews[i].Reachable = false
					return
				}
			}

			peerViews[i].ID = s.NodeID
			peerViews[i].State = s.State
			peerViews[i].Term = s.Term
			peerViews[i].IsLeader = s.IsLeader
			peerViews[i].Reachable = true
		}()
	}

	wg.Wait()

	// Build final list (self + peers) and sort by port for stable UI ordering.
	final := make([]nodeView, 0, 1+len(peerViews))
	final = append(final, nodeView{
		ID:        st.ID,
		State:     st.State,
		Term:      st.Term,
		IsLeader:  h.raft.IsLeader(),
		Reachable: true,
	})
	final = append(final, peerViews...)

	sort.Slice(final, func(i, j int) bool {
		_, pi := utils.ParseHostPort(final[i].ID)
		_, pj := utils.ParseHostPort(final[j].ID)
		if pi == pj {
			return final[i].ID < final[j].ID
		}
		return pi < pj
	})

	nodes := make([]map[string]interface{}, 0, len(final))
	for _, n := range final {
		nodes = append(nodes, map[string]interface{}{
			"id":        n.ID,
			"state":     n.State,
			"term":      n.Term,
			"is_leader": n.IsLeader,
			"reachable": n.Reachable,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"nodes": nodes,
	})
}

func (h *restAPIHandler) handleClusterStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	st := h.raft.Status()
	peers := h.raft.GetPeers()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"leader_id":  st.LeaderID,
		"term":       st.Term,
		"node_count": len(peers) + 1,
		"nodes":      append([]string{st.ID}, convertNodeIDsToStrings(peers)...),
	})
}

func (h *restAPIHandler) handleAddNode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !h.raft.IsLeader() {
		proxyToLeader(w, r, h.raft)
		return
	}

	var reqBody struct {
		NodeID string `json:"node_id"`
		Addr   string `json:"addr,omitempty"` // Optional, for future use
	}
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if reqBody.NodeID == "" {
		http.Error(w, "node_id required", http.StatusBadRequest)
		return
	}

	resp, err := h.raft.AddNode(r.Context(), &raftpb.AddNodeRequest{NodeId: reqBody.NodeID})
	if err != nil {
		writeError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if resp.Success {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusBadRequest)
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": resp.Success,
		"error":   resp.Error,
	})
}

func (h *restAPIHandler) handleRemoveNode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !h.raft.IsLeader() {
		proxyToLeader(w, r, h.raft)
		return
	}

	var reqBody struct {
		NodeID string `json:"node_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if reqBody.NodeID == "" {
		http.Error(w, "node_id required", http.StatusBadRequest)
		return
	}

	resp, err := h.raft.RemoveNode(r.Context(), &raftpb.RemoveNodeRequest{NodeId: reqBody.NodeID})
	if err != nil {
		writeError(w, err)
		return
	}

	// Best-effort: if this node was spawned by the dashboard, stop the child process so
	// it doesn't keep running as a standalone candidate after being removed.
	if resp.Success {
		_, port := utils.ParseHostPort(reqBody.NodeID)
		if port > 0 {
			// Try known process first (works if the current leader also spawned it).
			killed := false
			if cmd := spawner.get(port); cmd != nil && cmd.Process != nil {
				_ = cmd.Process.Kill()
				spawner.remove(port)
				killed = true
			}

			// Fallback: leadership may have changed since spawn, so this leader might not have
			// the pid in memory. Use pid file if present.
			dataDir := filepath.Join("data-spawn", fmt.Sprintf("node-%d", port))
			if !killed {
				if b, err := os.ReadFile(filepath.Join(dataDir, "pid")); err == nil {
					if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
						if p, err := os.FindProcess(pid); err == nil && p != nil {
							_ = p.Kill()
							killed = true
						}
					}
				}
			}

			// Best-effort cleanup of its data dir (also removes pid file).
			_ = os.RemoveAll(dataDir)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if resp.Success {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusBadRequest)
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": resp.Success,
		"error":   resp.Error,
	})
}

func (h *restAPIHandler) handleTransferLeadership(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !h.raft.IsLeader() {
		proxyToLeader(w, r, h.raft)
		return
	}

	var reqBody struct {
		TargetNodeID string `json:"target_node_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if reqBody.TargetNodeID == "" {
		http.Error(w, "target_node_id required", http.StatusBadRequest)
		return
	}

	resp, err := h.raft.TransferLeadership(r.Context(), &raftpb.TransferLeadershipRequest{TargetNodeId: reqBody.TargetNodeID})
	if err != nil {
		writeError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if resp.Success {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusBadRequest)
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": resp.Success,
		"error":   resp.Error,
	})
}

func (h *restAPIHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	st := h.raft.Status()
	peers := h.raft.GetPeers()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"node_id":        st.ID,
		"state":          st.State,
		"term":           st.Term,
		"leader_id":      st.LeaderID,
		"commit_index":   st.CommitIndex,
		"last_applied":   st.LastApplied,
		"log_length":     st.LogLength,
		"is_leader":      h.raft.IsLeader(),
		"has_read_lease": h.raft.HasReadLease(),
		"peers":          convertNodeIDsToStrings(peers),
		"peer_count":     len(peers),
	})
}

// handleSpawnNode launches a new mimorid process locally and adds it to the cluster.
func handleSpawnNode(w http.ResponseWriter, r *http.Request, raftNode *raft.Raft, grpcAddr string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !raftNode.IsLeader() {
		proxyToLeader(w, r, raftNode)
		return
	}

	// This endpoint spawns a new OS process and assumes the browser can reach it on host ports.
	// In Docker Compose, extra processes started inside the container will NOT have ports published,
	// so the dashboard can't navigate to them. Keep cluster growth in Docker explicit via compose.
	if dockerNodeIDRe.MatchString(raftNode.Status().ID) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "spawn-node is disabled in Docker Compose (ports are not published). Add more nodes via docker-compose instead.",
		})
		return
	}

	// Build reserved port list (self + peers + already spawned)
	_, selfPort := utils.ParseHostPort(grpcAddr)
	peers := raftNode.GetPeers()
	reserved := []int{selfPort}
	for _, p := range peers {
		_, rp := utils.ParseHostPort(string(p))
		if rp > 0 {
			reserved = append(reserved, rp)
		}
	}

	port, err := spawner.nextFreePort(reserved)
	if err != nil {
		http.Error(w, fmt.Sprintf("no free port: %v", err), http.StatusInternalServerError)
		return
	}

	// Prepare data dir (wipe if it already exists to avoid stale raft state across runs)
	dataDir := filepath.Join("data-spawn", fmt.Sprintf("node-%d", port))
	_ = os.RemoveAll(dataDir)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		http.Error(w, fmt.Sprintf("mkdir data dir: %v", err), http.StatusInternalServerError)
		return
	}

	// Choose a consistent node id:
	// - Local/manual mode: use browser host (usually "localhost") so UI shows "localhost:4002".
	// - Docker Compose mode: keep ids dialable inside the Docker network (e.g. "mimori-node1:4002").
	hostForIDs := requestHost(r)
	selfID := raftNode.Status().ID
	if dockerNodeIDRe.MatchString(selfID) {
		if h, _ := utils.ParseHostPort(selfID); h != "" {
			hostForIDs = h
		}
	}
	nodeID := fmt.Sprintf("%s:%d", hostForIDs, port)

	// Build peers list: include leader and known peers
	peerIDs := []string{string(normalizeNodeIDForHost(raft.NodeID(selfID), hostForIDs))}
	for _, p := range peers {
		peerIDs = append(peerIDs, string(normalizeNodeIDForHost(p, hostForIDs)))
	}
	peersEnv := strings.Join(peerIDs, ",")

	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("MIMORI_ADDR=:%d", port),
		fmt.Sprintf("MIMORI_NODE_ID=%s", nodeID),
		fmt.Sprintf("MIMORI_DATA=%s", dataDir),
		fmt.Sprintf("MIMORI_PEERS=%s", peersEnv),
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		http.Error(w, fmt.Sprintf("failed to start node: %v", err), http.StatusInternalServerError)
		return
	}

	// Persist pid so future leaders can stop the spawned process even if leadership changes.
	if cmd.Process != nil && cmd.Process.Pid > 0 {
		_ = os.WriteFile(filepath.Join(dataDir, "pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o644)
	}

	spawner.track(port, cmd)
	go func(p int, c *exec.Cmd) {
		_ = c.Wait()
		spawner.remove(p)
	}(port, cmd)

	// Propose add-node
	if err := raftNode.AddNodeInternal(raft.NodeID(nodeID)); err != nil {
		_ = cmd.Process.Kill()
		spawner.remove(port)
		writeLeaderJSONError(
			w,
			r,
			http.StatusInternalServerError,
			fmt.Sprintf("add node failed: %v", err),
			raftNode.LeaderID(),
		)
		return
	}

	resp := map[string]interface{}{
		"success": true,
		"node_id": nodeID,
		"addr":    nodeID,
		"data":    dataDir,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func writeError(w http.ResponseWriter, err error) {
	if st, ok := status.FromError(err); ok {
		code := http.StatusInternalServerError
		switch st.Code() {
		case codes.FailedPrecondition:
			code = http.StatusPreconditionFailed
		case codes.NotFound:
			code = http.StatusNotFound
		case codes.InvalidArgument:
			code = http.StatusBadRequest
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": st.Message(),
		})
		return
	}

	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func convertNodeIDsToStrings(ids []raft.NodeID) []string {
	result := make([]string, len(ids))
	for i, id := range ids {
		result[i] = string(id)
	}
	return result
}

// setupDashboard serves the dashboard static files
func setupDashboard(mux *http.ServeMux) {
	// Get the embedded filesystem, stripping the "web/dashboard" prefix
	fsys, err := fs.Sub(dashboardFS, "web/dashboard")
	if err != nil {
		logger := logging.With().Err(err).Str("component", "api").Logger()
		logger.Error().Msg("failed to setup dashboard filesystem")
		return
	}

	// Serve files from /dashboard/ prefix
	mux.Handle("/dashboard/", http.StripPrefix("/dashboard", http.FileServer(http.FS(fsys))))

	// Redirect root to dashboard
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/dashboard/", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})
}
