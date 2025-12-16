package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jerkeyray/mimori/internal/api/kv"
	raftpb "github.com/jerkeyray/mimori/internal/raft/raftpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// clientManager manages connections to MimoriDB nodes with:
// - Connection pooling (reuse connections)
// - Leader caching (remember current leader)
// - Automatic leader discovery (find leader automatically)
// - Retry logic (exponential backoff)
type clientManager struct {
	mu sync.RWMutex

	// Connection pool: map of address -> connection
	conns map[string]*grpc.ClientConn

	// Cached leader address (empty if unknown)
	leaderAddr string

	// Initial addresses to try (from --addr flag or env)
	initialAddrs []string

	// Connection timeout
	connTimeout time.Duration

	// Request timeout
	reqTimeout time.Duration
}

func splitAddrList(addrList string) []string {
	parts := strings.Split(addrList, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// newClientManager creates a new client manager.
func newClientManager(initialAddr string) *clientManager {
	initialAddrs := splitAddrList(initialAddr)
	if len(initialAddrs) == 0 {
		initialAddrs = []string{initialAddr}
	}
	return &clientManager{
		conns:        make(map[string]*grpc.ClientConn),
		initialAddrs: initialAddrs,
		connTimeout:  3 * time.Second,
		reqTimeout:   3 * time.Second,
	}
}

// getConnection returns a cached connection or creates a new one.
func (cm *clientManager) getConnection(addr string) (*grpc.ClientConn, error) {
	cm.mu.RLock()
	if conn, ok := cm.conns[addr]; ok {
		// Check if connection is still valid
		state := conn.GetState()
		if state.String() == "READY" || state.String() == "IDLE" {
			cm.mu.RUnlock()
			return conn, nil
		}
		// Connection is not ready, remove it
		cm.mu.RUnlock()
		cm.mu.Lock()
		delete(cm.conns, addr)
		cm.mu.Unlock()
	} else {
		cm.mu.RUnlock()
	}

	// Create new connection
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Double-check after acquiring write lock
	if conn, ok := cm.conns[addr]; ok {
		return conn, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), cm.connTimeout)
	defer cancel()

	conn, err := grpc.DialContext(
		ctx,
		addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to %s: %w", addr, err)
	}

	cm.conns[addr] = conn
	return conn, nil
}

// discoverLeader finds the current leader by querying nodes.
func (cm *clientManager) discoverLeader() (string, error) {
	// Try cached leader first
	cm.mu.RLock()
	if cm.leaderAddr != "" {
		leader := cm.leaderAddr
		cm.mu.RUnlock()
		if err := cm.verifyLeader(leader); err == nil {
			return leader, nil
		}
		// Cached leader is stale, clear it
		cm.mu.Lock()
		cm.leaderAddr = ""
		cm.mu.Unlock()
	} else {
		cm.mu.RUnlock()
	}

	// Try all known addresses
	addrsToTry := make([]string, 0, len(cm.initialAddrs)+len(cm.conns))
	addrsToTry = append(addrsToTry, cm.initialAddrs...)

	cm.mu.RLock()
	for addr := range cm.conns {
		addrsToTry = append(addrsToTry, addr)
	}
	cm.mu.RUnlock()

	// Try each address
	for _, addr := range addrsToTry {
		leader, err := cm.findLeaderFromNode(addr)
		if err == nil && leader != "" {
			cm.mu.Lock()
			cm.leaderAddr = leader
			cm.mu.Unlock()
			return leader, nil
		}
	}

	return "", fmt.Errorf("could not discover leader from any node")
}

// findLeaderFromNode queries a node to find the leader using HTTP status endpoint.
func (cm *clientManager) findLeaderFromNode(addr string) (string, error) {
	// Use HTTP endpoint to get status
	httpAddr := getHTTPAddr(addr)
	url := fmt.Sprintf("http://%s/raft/status", httpAddr)

	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status check failed: %d", resp.StatusCode)
	}

	var status struct {
		ID       string `json:"id"`
		State    string `json:"state"`
		LeaderID string `json:"leader_id"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return "", err
	}

	if status.State == "leader" {
		return addr, nil
	}

	if status.LeaderID != "" {
		return normalizeLeaderAddr(addr, status.LeaderID), nil
	}

	return "", fmt.Errorf("node %s does not know the leader", addr)
}

// verifyLeader checks if the given address is still the leader using HTTP.
func (cm *clientManager) verifyLeader(addr string) error {
	httpAddr := getHTTPAddr(addr)
	url := fmt.Sprintf("http://%s/raft/status", httpAddr)

	client := http.Client{Timeout: 1 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status check failed")
	}

	var status struct {
		State string `json:"state"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return err
	}

	if status.State != "leader" {
		return fmt.Errorf("node is not leader")
	}

	return nil
}

// getHTTPAddr converts a gRPC address to HTTP address (port + 1).
func getHTTPAddr(grpcAddr string) string {
	// Simple conversion: if port is 4000, HTTP is 4001
	// Format: "host:port" -> "host:port+1"
	host, portStr, ok := strings.Cut(grpcAddr, ":")
	if !ok {
		return grpcAddr // Return as-is if can't parse
	}

	port, err := strconv.Atoi(portStr)
	if err != nil {
		return grpcAddr
	}

	return fmt.Sprintf("%s:%d", host, port+1)
}

// normalizeLeaderAddr turns a leader hint (often ":4000" or "mimori-node3:4000")
// into a dialable address.
func normalizeLeaderAddr(baseAddr, leaderID string) string {
	leaderID = strings.TrimSpace(leaderID)
	if leaderID == "" {
		return ""
	}

	// Derive the "base" host from the address we successfully used.
	baseHost, _, ok := strings.Cut(baseAddr, ":")
	if !ok {
		baseHost = ""
	}

	// Heuristic: when the base host is localhost/loopback, we assume we are outside
	// the cluster (e.g. talking to Docker containers via forwarded ports). In that
	// case, leader IDs like "mimori-node3:4000" or ":4000" should be mapped back
	// onto the base host + the leader's port so we dial 127.0.0.1:4000 instead of
	// trying to resolve "mimori-node3" on the host OS.
	useBaseHost := baseHost == "" || baseHost == "localhost" || strings.HasPrefix(baseHost, "127.")

	// If leaderID has no colon or starts with ':' -> just graft the port onto baseHost.
	if strings.HasPrefix(leaderID, ":") {
		// leaderID like ":4000"
		if useBaseHost && baseHost != "" {
			return baseHost + leaderID
		}
		return leaderID
	}

	if !strings.Contains(leaderID, ":") {
		// leaderID like "4000"
		if useBaseHost && baseHost != "" {
			return fmt.Sprintf("%s:%s", baseHost, leaderID)
		}
		// No sensible host to attach, return as-is.
		return ":" + leaderID
	}

	// leaderID looks like "host:port" – decide whether to trust its host.
	leaderHost, port, ok := strings.Cut(leaderID, ":")
	if !ok || port == "" {
		// Malformed; fall back to previous rules.
		if useBaseHost && baseHost != "" {
			return baseHost
		}
		return leaderID
	}

	if useBaseHost && baseHost != "" {
		// Outside the cluster: always dial baseHost:leaderPort.
		return fmt.Sprintf("%s:%s", baseHost, port)
	}

	// Inside the cluster (e.g. running inside Docker network): keep the leader's host.
	if leaderHost == "" && baseHost != "" {
		return fmt.Sprintf("%s:%s", baseHost, port)
	}
	return leaderID
}

// getLeader returns the current leader address, discovering it if needed.
func (cm *clientManager) getLeader() (string, error) {
	cm.mu.RLock()
	cached := cm.leaderAddr
	cm.mu.RUnlock()

	if cached != "" {
		if err := cm.verifyLeader(cached); err == nil {
			return cached, nil
		}
		// Cached leader is stale
		cm.mu.Lock()
		cm.leaderAddr = ""
		cm.mu.Unlock()
	}

	// Discover leader
	return cm.discoverLeader()
}

// executeWithRetry executes a function with retry logic and exponential backoff.
func (cm *clientManager) executeWithRetry(
	fn func(addr string) error,
	maxRetries int,
) error {
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		// Get leader address
		leaderAddr, err := cm.getLeader()
		if err != nil {
			// If we can't find leader, try initial addresses
			if attempt < len(cm.initialAddrs) {
				leaderAddr = cm.initialAddrs[attempt%len(cm.initialAddrs)]
			} else {
				lastErr = err
				time.Sleep(time.Duration(attempt+1) * 100 * time.Millisecond)
				continue
			}
		}

		// Execute the function
		err = fn(leaderAddr)
		if err == nil {
			return nil
		}

		// Check if error indicates we should retry with different leader
		if isLeaderError(err) {
			// Prefer fast-following the leader hinted in the error (if present),
			// otherwise clear cached leader and re-discover.
			if hinted, ok := extractLeaderHint(err); ok {
				hinted = normalizeLeaderAddr(leaderAddr, hinted)
				cm.mu.Lock()
				cm.leaderAddr = hinted
				cm.mu.Unlock()
			} else {
				cm.mu.Lock()
				cm.leaderAddr = ""
				cm.mu.Unlock()
			}
			lastErr = err
			// Short delay before retry
			time.Sleep(time.Duration(attempt+1) * 50 * time.Millisecond)
			continue
		}

		// For other errors, use exponential backoff
		lastErr = err
		backoff := time.Duration(1<<uint(attempt)) * 100 * time.Millisecond
		if backoff > 2*time.Second {
			backoff = 2 * time.Second
		}
		time.Sleep(backoff)
	}

	return fmt.Errorf("operation failed after %d retries: %w", maxRetries, lastErr)
}

// isLeaderError checks if an error indicates a leader redirect is needed.
func isLeaderError(err error) bool {
	if err == nil {
		return false
	}

	st, ok := status.FromError(err)
	if !ok {
		return false
	}

	// Server uses FailedPrecondition for "not leader" and similar redirects.
	if st.Code() == codes.FailedPrecondition {
		msg := strings.ToLower(st.Message())
		return strings.Contains(msg, "not leader") || strings.Contains(msg, "leader=")
	}

	return false
}

// extractLeaderHint extracts "leader=..." from gRPC error messages emitted by the server.
func extractLeaderHint(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	st, ok := status.FromError(err)
	if !ok {
		return "", false
	}

	msg := st.Message()
	idx := strings.Index(msg, "leader=")
	if idx == -1 {
		return "", false
	}

	leader := strings.TrimSpace(msg[idx+len("leader="):])
	if leader == "" {
		return "", false
	}

	// Stop at common delimiters.
	for i, r := range leader {
		switch r {
		case ' ', '\t', '\n', ')', ',', ';':
			leader = leader[:i]
			goto done
		}
	}
done:
	leader = strings.TrimSpace(leader)
	if leader == "" {
		return "", false
	}
	return leader, true
}

// getKVClient returns a KV client for the given address.
func (cm *clientManager) getKVClient(addr string) (kv.KVClient, error) {
	conn, err := cm.getConnection(addr)
	if err != nil {
		return nil, err
	}
	return kv.NewKVClient(conn), nil
}

// getRaftClient returns a Raft client for the given address.
func (cm *clientManager) getRaftClient(addr string) (raftpb.RaftClient, error) {
	conn, err := cm.getConnection(addr)
	if err != nil {
		return nil, err
	}
	return raftpb.NewRaftClient(conn), nil
}

// close closes all connections.
func (cm *clientManager) close() {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	for addr, conn := range cm.conns {
		conn.Close()
		delete(cm.conns, addr)
	}
}

// Global client manager instance
var globalClientManager *clientManager
var clientManagerOnce sync.Once

// getClientManager returns the global client manager instance.
func getClientManager() *clientManager {
	clientManagerOnce.Do(func() {
		// Prefer env-provided seeds if present so you rarely need --addr:
		//   MIMORI_ADDRS=host1:4000,host2:4000 mimorictl get k
		//   MIMORI_SEEDS=... (alias env var)
		seed := addr
		if v := os.Getenv("MIMORI_ADDRS"); v != "" {
			seed = v
		}
		if v := os.Getenv("MIMORI_SEEDS"); v != "" {
			seed = v
		}
		globalClientManager = newClientManager(seed)
	})
	return globalClientManager
}
