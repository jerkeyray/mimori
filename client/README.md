# Mimori Go Client Library

Production-ready Go client for Mimori distributed key-value store.

## Features

- Automatic leader discovery and caching
- Connection pooling and reuse
- Automatic retries with exponential backoff
- Context support for timeouts and cancellation
- Thread-safe (safe for concurrent use)
- Clean error handling

## Installation

```bash
go get github.com/jerkeyray/mimori
```

## Quick Start

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/jerkeyray/mimori/client"
)

func main() {
    // Create client with seed addresses
    c, err := client.New([]string{"localhost:4000", "localhost:4002", "localhost:4004"})
    if err != nil {
        log.Fatal(err)
    }
    defer c.Close()

    ctx := context.Background()

    // Put a key-value pair (always goes to leader)
    if err := c.Put(ctx, []byte("key"), []byte("value")); err != nil {
        log.Fatal(err)
    }

    // Get a value (strong read from leader)
    value, found, err := c.Get(ctx, []byte("key"))
    if err != nil {
        log.Fatal(err)
    }
    if found {
        fmt.Printf("Value: %s\n", value)
    }

    // Delete a key
    if err := c.Delete(ctx, []byte("key")); err != nil {
        log.Fatal(err)
    }
}
```

## API Reference

### Creating a Client

```go
// Simple creation with seeds
c, err := client.New([]string{"localhost:4000"})

// Custom configuration
cfg := client.Config{
    Seeds:       []string{"localhost:4000", "localhost:4002"},
    ConnTimeout: 5 * time.Second,  // gRPC connection timeout
    ReqTimeout:  10 * time.Second, // per-request timeout
    MaxRetries:  5,                // retry attempts
}
c, err := client.NewWithConfig(cfg)
```

### Operations

```go
// Put: store a key-value pair (always goes to leader)
err := c.Put(ctx, []byte("key"), []byte("value"))

// Get: retrieve a value (strong read from leader by default)
value, found, err := c.Get(ctx, []byte("key"))

// Get with stale reads: can read from followers (lower latency, may be stale)
value, found, err := c.GetWithOptions(ctx, []byte("key"), client.GetOptions{
    AllowStale: true,
})

// Delete: remove a key (always goes to leader)
err := c.Delete(ctx, []byte("key"))

// Health: check cluster connectivity
err := c.Health(ctx)

// Close: release all connections
err := c.Close()
```

## Consistency Guarantees

### Strong Reads (Default)

```go
value, found, err := c.Get(ctx, key)
```

- Reads from the Raft leader
- Linearizable consistency
- Always returns the latest committed value

### Stale Reads (Follower Reads)

```go
value, found, err := c.GetWithOptions(ctx, key, client.GetOptions{
    AllowStale: true,
})
```

- Can read from followers (if they have a valid read lease)
- May return data up to ~300ms stale
- Better throughput and lower latency

## Error Handling

The client automatically retries on leader changes and transient errors. Final errors are returned as standard Go errors:

```go
if err := c.Put(ctx, key, value); err != nil {
    // Handle error
    log.Printf("put failed: %v", err)
}
```

## Context Support

All operations accept a `context.Context` for timeouts and cancellation:

```go
// Per-request timeout
ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
defer cancel()

err := c.Put(ctx, key, value)
```

## Thread Safety

The `Client` type is safe for concurrent use by multiple goroutines. You can share a single client instance across your application.

## Examples

See `example_test.go` for more examples, including:

- Web session storage
- Feature flags
- Distributed configuration
