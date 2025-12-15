package raft

import (
	"context"
	"io"
	"sync"
	"time"

	raftpb "github.com/jerkeyray/mimori/internal/raft/raftpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
)

// connectionPool manages a pool of gRPC connections to peer nodes.
// Connections are cached and reused to avoid the overhead of creating
// new connections for every RPC call.
type connectionPool struct {
	mu         sync.RWMutex
	conns      map[string]*pooledConnection
	dialer     func(addr string) (raftpb.RaftClient, io.Closer, error)
	shutdownCh chan struct{}
}

type pooledConnection struct {
	conn   *grpc.ClientConn
	client raftpb.RaftClient
	mu     sync.Mutex
	refs   int // Reference count for this connection
}

// newConnectionPool creates a new connection pool.
func newConnectionPool(customDialer func(addr string) (raftpb.RaftClient, io.Closer, error)) *connectionPool {
	return &connectionPool{
		conns:      make(map[string]*pooledConnection),
		dialer:     customDialer,
		shutdownCh: make(chan struct{}),
	}
}

// getConnection returns a cached connection or creates a new one.
// The caller should call releaseConnection when done with the connection.
func (p *connectionPool) getConnection(addr string) (raftpb.RaftClient, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Check if we have a cached connection
	if pooled, ok := p.conns[addr]; ok {
		pooled.mu.Lock()
		// Check if connection is still healthy
		state := pooled.conn.GetState()
		if state == connectivity.Ready || state == connectivity.Idle {
			pooled.refs++
			pooled.mu.Unlock()
			return pooled.client, nil
		}
		// Connection is not ready, remove it and create a new one
		pooled.mu.Unlock()
		pooled.conn.Close()
		delete(p.conns, addr)
	}

	// Create new connection
	var client raftpb.RaftClient
	var conn *grpc.ClientConn
	var err error

	if p.dialer != nil {
		// Use custom dialer (e.g., for testing)
		client, _, err = p.dialer(addr)
		if err != nil {
			return nil, err
		}
		// For custom dialers, we can't pool the connection
		// as it returns an io.Closer interface, not *grpc.ClientConn
		// Return the client directly (caller will handle closing)
		return client, nil
	}

	// Default dialer: create gRPC connection
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	conn, err = grpc.DialContext(
		ctx,
		addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(), // Wait for connection to be established
	)
	if err != nil {
		return nil, err
	}

	client = raftpb.NewRaftClient(conn)

	// Cache the connection
	pooled := &pooledConnection{
		conn:   conn,
		client: client,
		refs:   1,
	}
	p.conns[addr] = pooled

	return client, nil
}

// releaseConnection decrements the reference count for a connection.
// In the current implementation, connections are kept alive for reuse,
// but this method is here for future optimization (e.g., closing idle connections).
func (p *connectionPool) releaseConnection(addr string) {
	p.mu.RLock()
	pooled, ok := p.conns[addr]
	p.mu.RUnlock()

	if !ok {
		return
	}

	pooled.mu.Lock()
	defer pooled.mu.Unlock()

	if pooled.refs > 0 {
		pooled.refs--
	}

	// Note: We don't close connections here to allow reuse.
	// Connections will be closed when:
	// 1. Peer is removed from cluster
	// 2. Node shuts down
	// 3. Connection becomes unhealthy
}

// removeConnection closes and removes a connection for a given peer address.
// This should be called when a peer is removed from the cluster.
func (p *connectionPool) removeConnection(addr string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if pooled, ok := p.conns[addr]; ok {
		pooled.conn.Close()
		delete(p.conns, addr)
	}
}

// closeAll closes all cached connections. Should be called during shutdown.
func (p *connectionPool) closeAll() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for addr, pooled := range p.conns {
		pooled.conn.Close()
		delete(p.conns, addr)
	}
}
