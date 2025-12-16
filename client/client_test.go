package client

import (
	"context"
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	// Should fail with no seeds
	_, err := New(nil)
	if err == nil {
		t.Fatal("expected error with no seeds")
	}

	// Should succeed with seeds
	c, err := New([]string{"localhost:4000"})
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	defer c.Close()

	if len(c.seeds) != 1 {
		t.Fatalf("expected 1 seed, got %d", len(c.seeds))
	}
}

func TestNewWithConfig(t *testing.T) {
	cfg := Config{
		Seeds:       []string{"localhost:4000", "localhost:4002"},
		ConnTimeout: 5 * time.Second,
		ReqTimeout:  10 * time.Second,
		MaxRetries:  5,
	}

	c, err := NewWithConfig(cfg)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	defer c.Close()

	if c.connTimeout != 5*time.Second {
		t.Fatalf("expected connTimeout 5s, got %v", c.connTimeout)
	}
	if c.reqTimeout != 10*time.Second {
		t.Fatalf("expected reqTimeout 10s, got %v", c.reqTimeout)
	}
	if c.maxRetries != 5 {
		t.Fatalf("expected maxRetries 5, got %d", c.maxRetries)
	}
}

func TestNormalizeLeaderAddr(t *testing.T) {
	tests := []struct {
		name     string
		base     string
		leaderID string
		want     string
	}{
		{
			name:     "colon-port",
			base:     "127.0.0.1:4002",
			leaderID: ":4000",
			want:     "127.0.0.1:4000",
		},
		{
			name:     "port-only",
			base:     "localhost:4002",
			leaderID: "4000",
			want:     "localhost:4000",
		},
		{
			name:     "full-address-loopback-base",
			base:     "127.0.0.1:4002",
			leaderID: "mimori-node2:4000",
			want:     "127.0.0.1:4000",
		},
		{
			name:     "full-address-non-loopback-base",
			base:     "10.0.1.5:4002",
			leaderID: "mimori-node2:4000",
			want:     "mimori-node2:4000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeLeaderAddr(tt.base, tt.leaderID)
			if got != tt.want {
				t.Fatalf("normalizeLeaderAddr(%q, %q) = %q, want %q", tt.base, tt.leaderID, got, tt.want)
			}
		})
	}
}

func TestGetHTTPAddr(t *testing.T) {
	tests := []struct {
		grpc string
		want string
	}{
		{"localhost:4000", "localhost:4001"},
		{"127.0.0.1:4002", "127.0.0.1:4003"},
		{":4004", ":4005"},
	}

	for _, tt := range tests {
		t.Run(tt.grpc, func(t *testing.T) {
			got := getHTTPAddr(tt.grpc)
			if got != tt.want {
				t.Fatalf("getHTTPAddr(%q) = %q, want %q", tt.grpc, got, tt.want)
			}
		})
	}
}

// Integration test (requires running cluster)
func TestClientIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx := context.Background()

	// Create client pointing to local cluster
	c, err := New([]string{"127.0.0.1:4000", "127.0.0.1:4002", "127.0.0.1:4004"})
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	defer c.Close()

	// Health check
	if err := c.Health(ctx); err != nil {
		t.Skipf("cluster not available: %v", err)
	}

	// Put
	key := []byte("test-key")
	value := []byte("test-value")
	if err := c.Put(ctx, key, value); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Get
	got, found, err := c.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !found {
		t.Fatal("key not found")
	}
	if string(got) != string(value) {
		t.Fatalf("Get returned %q, want %q", got, value)
	}

	// Delete
	if err := c.Delete(ctx, key); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Verify deleted
	_, found, err = c.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get after delete failed: %v", err)
	}
	if found {
		t.Fatal("key should not be found after delete")
	}
}
