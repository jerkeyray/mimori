package client_test

import (
	"context"
	"fmt"
	"log"

	"github.com/jerkeyray/mimori/client"
)

// This example shows basic usage of the Mimori client library.
// Note: This example requires a running Mimori cluster.
func Example_basic() {
	// Create a client with seed addresses
	c, err := client.New([]string{"localhost:4000", "localhost:4002", "localhost:4004"})
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	ctx := context.Background()

	// Put a key-value pair
	if err := c.Put(ctx, []byte("user:123"), []byte(`{"name":"alice"}`)); err != nil {
		log.Fatal(err)
	}

	// Get a value
	value, found, err := c.Get(ctx, []byte("user:123"))
	if err != nil {
		log.Fatal(err)
	}
	if found {
		fmt.Printf("Value: %s\n", value)
	}

	// Delete a key
	if err := c.Delete(ctx, []byte("user:123")); err != nil {
		log.Fatal(err)
	}
}

// This example shows how to use GetWithOptions for stale reads.
func ExampleClient_GetWithOptions() {
	c, _ := client.New([]string{"localhost:4000"})
	defer c.Close()

	ctx := context.Background()

	// Stale read from followers (lower latency, may be slightly stale)
	_, _, err := c.GetWithOptions(ctx, []byte("key"), client.GetOptions{
		AllowStale: true,
	})
	if err != nil {
		log.Fatal(err)
	}
}

// This example shows how to create a client with custom configuration.
func ExampleNewWithConfig() {
	// Custom configuration
	cfg := client.Config{
		Seeds:       []string{"localhost:4000", "localhost:4002"},
		ConnTimeout: 5 * 1000000000,  // 5 seconds
		ReqTimeout:  10 * 1000000000, // 10 seconds
		MaxRetries:  5,
	}

	c, err := client.NewWithConfig(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	ctx := context.Background()

	// Use the client
	_ = c.Put(ctx, []byte("key"), []byte("value"))
}
