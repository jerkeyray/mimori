// Package client provides a Go client library for Mimori distributed key-value store.
//
// The client handles leader discovery, connection pooling, automatic retries, and
// graceful error handling, making it simple to embed Mimori into Go applications.
//
// Example usage:
//
//	c, err := client.New([]string{"localhost:4000", "localhost:4002", "localhost:4004"})
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer c.Close()
//
//	// Put a key-value pair
//	if err := c.Put(ctx, []byte("user:123"), []byte(`{"name":"alice"}`)); err != nil {
//	    log.Fatal(err)
//	}
//
//	// Get a value
//	val, found, err := c.Get(ctx, []byte("user:123"))
//	if err != nil {
//	    log.Fatal(err)
//	}
//	if found {
//	    fmt.Printf("Value: %s\n", val)
//	}
//
//	// Delete a key
//	if err := c.Delete(ctx, []byte("user:123")); err != nil {
//	    log.Fatal(err)
//	}
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jerkeyray/mimori/internal/api/kv"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// Client is a Mimori client that handles leader discovery, connection pooling,
// and automatic retries.
type Client struct {
	mu sync.RWMutex

	// Connection pool: map of address -> connection
	conns map[string]*grpc.ClientConn

	// Cached leader address (empty if unknown)
	leaderAddr string

	// Initial seed addresses
	seeds []string

	// Timeouts
	connTimeout time.Duration
	reqTimeout  time.Duration

	// Retry config
	maxRetries int
}

// Config holds configuration options for a Mimori client.
type Config struct {
	// Seeds are the initial cluster addresses to try (required).
	Seeds []string

	// ConnTimeout is the timeout for establishing gRPC connections.
	// Default: 3 seconds.
	ConnTimeout time.Duration

	// ReqTimeout is the default timeout for individual requests.
	// Default: 5 seconds.
	ReqTimeout time.Duration

	// MaxRetries is the maximum number of retries for failed operations.
	// Default: 3.
	MaxRetries int
}

// New creates a new Mimori client with the given seed addresses.
// Seeds should be gRPC addresses like "localhost:4000" or "127.0.0.1:4002".
func New(seeds []string) (*Client, error) {
	return NewWithConfig(Config{Seeds: seeds})
}

// NewWithConfig creates a new Mimori client with custom configuration.
func NewWithConfig(cfg Config) (*Client, error) {
	if len(cfg.Seeds) == 0 {
		return nil, fmt.Errorf("at least one seed address is required")
	}

	if cfg.ConnTimeout == 0 {
		cfg.ConnTimeout = 3 * time.Second
	}
	if cfg.ReqTimeout == 0 {
		cfg.ReqTimeout = 5 * time.Second
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 3
	}

	return &Client{
		conns:       make(map[string]*grpc.ClientConn),
		seeds:       cfg.Seeds,
		connTimeout: cfg.ConnTimeout,
		reqTimeout:  cfg.ReqTimeout,
		maxRetries:  cfg.MaxRetries,
	}, nil
}

// Put stores a key-value pair in the cluster.
// The write is always sent to the Raft leader.
func (c *Client) Put(ctx context.Context, key, value []byte) error {
	return c.executeWithRetry(ctx, func(ctx context.Context, addr string) error {
		client, err := c.getKVClient(addr)
		if err != nil {
			return err
		}

		_, err = client.Put(ctx, &kv.PutRequest{
			Key:   key,
			Value: value,
		})
		return err
	})
}

// Get retrieves a value for a key from the cluster.
// By default, reads go to the leader for strong consistency.
func (c *Client) Get(ctx context.Context, key []byte) (value []byte, found bool, err error) {
	return c.GetWithOptions(ctx, key, GetOptions{})
}

// GetOptions configures a Get operation.
type GetOptions struct {
	// AllowStale allows reads from followers (may return stale data).
	// Default: false (reads go to leader).
	AllowStale bool
}

// GetWithOptions retrieves a value with custom options.
func (c *Client) GetWithOptions(ctx context.Context, key []byte, opts GetOptions) (value []byte, found bool, err error) {
	var resp *kv.GetResponse
	err = c.executeWithRetry(ctx, func(ctx context.Context, addr string) error {
		client, err := c.getKVClient(addr)
		if err != nil {
			return err
		}

		resp, err = client.Get(ctx, &kv.GetRequest{
			Key:        key,
			AllowStale: opts.AllowStale,
		})
		return err
	})

	if err != nil {
		return nil, false, err
	}
	return resp.Value, resp.Found, nil
}

// Delete removes a key from the cluster.
// The delete is always sent to the Raft leader.
func (c *Client) Delete(ctx context.Context, key []byte) error {
	return c.executeWithRetry(ctx, func(ctx context.Context, addr string) error {
		client, err := c.getKVClient(addr)
		if err != nil {
			return err
		}

		_, err = client.Delete(ctx, &kv.DeleteRequest{Key: key})
		return err
	})
}

// Health checks if the cluster is reachable and responsive.
func (c *Client) Health(ctx context.Context) error {
	return c.executeWithRetry(ctx, func(ctx context.Context, addr string) error {
		client, err := c.getKVClient(addr)
		if err != nil {
			return err
		}

		_, err = client.Health(ctx, &kv.HealthRequest{})
		return err
	})
}

// Close closes all open connections to the cluster.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	for addr, conn := range c.conns {
		conn.Close()
		delete(c.conns, addr)
	}
	return nil
}

// executeWithRetry executes a function with automatic retry and leader discovery.
func (c *Client) executeWithRetry(ctx context.Context, fn func(context.Context, string) error) error {
	var lastErr error

	for attempt := 0; attempt < c.maxRetries; attempt++ {
		// Get leader address
		leaderAddr, err := c.getLeader(ctx)
		if err != nil {
			// If we can't find leader, try seeds
			if attempt < len(c.seeds) {
				leaderAddr = c.seeds[attempt%len(c.seeds)]
			} else {
				lastErr = err
				time.Sleep(time.Duration(attempt+1) * 100 * time.Millisecond)
				continue
			}
		}

		// Execute with timeout
		reqCtx := ctx
		if _, hasDeadline := ctx.Deadline(); !hasDeadline {
			var cancel context.CancelFunc
			reqCtx, cancel = context.WithTimeout(ctx, c.reqTimeout)
			defer cancel()
		}

		err = fn(reqCtx, leaderAddr)
		if err == nil {
			return nil
		}

		// Check if error indicates we should retry with different leader
		if isLeaderError(err) {
			// Try to extract and normalize leader hint
			if hinted, ok := extractLeaderHint(err); ok {
				// Use first seed address as base for normalization (ensures Docker hostname mapping works)
				baseAddr := leaderAddr
				if len(c.seeds) > 0 {
					baseAddr = c.seeds[0]
				}
				hinted = normalizeLeaderAddr(baseAddr, hinted)
				c.mu.Lock()
				c.leaderAddr = hinted
				c.mu.Unlock()
			} else {
				c.mu.Lock()
				c.leaderAddr = ""
				c.mu.Unlock()
			}
			lastErr = err
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

	return fmt.Errorf("operation failed after %d retries: %w", c.maxRetries, lastErr)
}

// getConnection returns a cached connection or creates a new one.
func (c *Client) getConnection(addr string) (*grpc.ClientConn, error) {
	c.mu.RLock()
	if conn, ok := c.conns[addr]; ok {
		state := conn.GetState()
		if state.String() == "READY" || state.String() == "IDLE" {
			c.mu.RUnlock()
			return conn, nil
		}
		c.mu.RUnlock()
		c.mu.Lock()
		delete(c.conns, addr)
		c.mu.Unlock()
	} else {
		c.mu.RUnlock()
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if conn, ok := c.conns[addr]; ok {
		return conn, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.connTimeout)
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

	c.conns[addr] = conn
	return conn, nil
}

// getKVClient returns a KV client for the given address.
func (c *Client) getKVClient(addr string) (kv.KVClient, error) {
	conn, err := c.getConnection(addr)
	if err != nil {
		return nil, err
	}
	return kv.NewKVClient(conn), nil
}

// getLeader returns the current leader address.
func (c *Client) getLeader(ctx context.Context) (string, error) {
	c.mu.RLock()
	cached := c.leaderAddr
	c.mu.RUnlock()

	if cached != "" {
		if err := c.verifyLeader(ctx, cached); err == nil {
			return cached, nil
		}
		c.mu.Lock()
		c.leaderAddr = ""
		c.mu.Unlock()
	}

	return c.discoverLeader(ctx)
}

// discoverLeader finds the current leader by querying seed nodes.
func (c *Client) discoverLeader(ctx context.Context) (string, error) {
	for _, seed := range c.seeds {
		leader, err := c.findLeaderFromNode(ctx, seed)
		if err == nil && leader != "" {
			c.mu.Lock()
			c.leaderAddr = leader
			c.mu.Unlock()
			return leader, nil
		}
	}
	return "", fmt.Errorf("could not discover leader from any seed")
}

// findLeaderFromNode queries a node to find the leader using HTTP status endpoint.
func (c *Client) findLeaderFromNode(ctx context.Context, addr string) (string, error) {
	httpAddr := getHTTPAddr(addr)
	url := fmt.Sprintf("http://%s/raft/status", httpAddr)

	client := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}

	resp, err := client.Do(req)
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

// verifyLeader checks if the given address is still the leader.
func (c *Client) verifyLeader(ctx context.Context, addr string) error {
	httpAddr := getHTTPAddr(addr)
	url := fmt.Sprintf("http://%s/raft/status", httpAddr)

	client := &http.Client{Timeout: 1 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := client.Do(req)
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
	parts := strings.Split(grpcAddr, ":")
	if len(parts) != 2 {
		return grpcAddr
	}

	port, err := strconv.Atoi(parts[1])
	if err != nil {
		return grpcAddr
	}

	return fmt.Sprintf("%s:%d", parts[0], port+1)
}

// normalizeLeaderAddr turns a leader hint into a dialable address.
func normalizeLeaderAddr(baseAddr, leaderID string) string {
	leaderID = strings.TrimSpace(leaderID)
	if leaderID == "" {
		return ""
	}

	baseHost, _, ok := strings.Cut(baseAddr, ":")
	if !ok {
		baseHost = ""
	}

	useBaseHost := baseHost == "" || baseHost == "localhost" || strings.HasPrefix(baseHost, "127.")

	if strings.HasPrefix(leaderID, ":") {
		if useBaseHost && baseHost != "" {
			return baseHost + leaderID
		}
		return leaderID
	}

	if !strings.Contains(leaderID, ":") {
		if useBaseHost && baseHost != "" {
			return fmt.Sprintf("%s:%s", baseHost, leaderID)
		}
		return ":" + leaderID
	}

	leaderHost, port, ok := strings.Cut(leaderID, ":")
	if !ok || port == "" {
		if useBaseHost && baseHost != "" {
			return baseHost
		}
		return leaderID
	}

	if useBaseHost && baseHost != "" {
		return fmt.Sprintf("%s:%s", baseHost, port)
	}

	if leaderHost == "" && baseHost != "" {
		return fmt.Sprintf("%s:%s", baseHost, port)
	}
	return leaderID
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

	if st.Code() == codes.FailedPrecondition {
		msg := strings.ToLower(st.Message())
		return strings.Contains(msg, "not leader") || strings.Contains(msg, "leader=")
	}

	return false
}

// extractLeaderHint extracts "leader=..." from gRPC error messages.
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
