package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/jerkeyray/mimori/client"
)

func main() {
	// Create a Mimori client
	// Provide multiple seed addresses for high availability
	seeds := []string{
		"localhost:4000",
		"localhost:4002",
		"localhost:4004",
	}

	c, err := client.New(seeds)
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}
	defer c.Close()

	fmt.Println("Connected to Mimori cluster")

	ctx := context.Background()

	// 1. Health check
	fmt.Println("\n[1] Health check...")
	if err := c.Health(ctx); err != nil {
		log.Fatalf("Health check failed: %v", err)
	}
	fmt.Println("    Cluster is healthy")

	// 2. Put operations
	fmt.Println("\n[2] Writing data...")
	examples := map[string]string{
		"user:alice": `{"name":"Alice","role":"admin"}`,
		"user:bob":   `{"name":"Bob","role":"user"}`,
		"config:ttl": "300",
	}

	for key, value := range examples {
		if err := c.Put(ctx, []byte(key), []byte(value)); err != nil {
			log.Fatalf("Put failed for %s: %v", key, err)
		}
		fmt.Printf("    Wrote: %s\n", key)
	}

	// 3. Strong reads (from leader)
	fmt.Println("\n[3] Reading data (strong consistency)...")
	value, found, err := c.Get(ctx, []byte("user:alice"))
	if err != nil {
		log.Fatalf("Get failed: %v", err)
	}
	if found {
		fmt.Printf("    user:alice = %s\n", value)
	}

	// 4. Stale reads (from followers)
	fmt.Println("\n[4] Reading data (allow stale)...")
	value, found, err = c.GetWithOptions(ctx, []byte("config:ttl"), client.GetOptions{
		AllowStale: true,
	})
	if err != nil {
		log.Fatalf("Get with stale failed: %v", err)
	}
	if found {
		fmt.Printf("    config:ttl = %s (may be slightly stale)\n", value)
	}

	// 5. Delete
	fmt.Println("\n[5] Deleting data...")
	if err := c.Delete(ctx, []byte("user:bob")); err != nil {
		log.Fatalf("Delete failed: %v", err)
	}
	fmt.Println("    Deleted: user:bob")

	// 6. Verify deletion
	_, found, err = c.Get(ctx, []byte("user:bob"))
	if err != nil {
		log.Fatalf("Get after delete failed: %v", err)
	}
	if !found {
		fmt.Println("    Verified: user:bob no longer exists")
	}

	// 7. Timeout example (demonstrating timeout handling)
	fmt.Println("\n[6] Using context timeout...")
	timeoutCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	if err := c.Put(timeoutCtx, []byte("fast"), []byte("data")); err != nil {
		fmt.Printf("    ✓ Timeout respected (100ms too short): %v\n", err)
	} else {
		fmt.Println("    ✓ Put succeeded within timeout")
	}

	// Now with a reasonable timeout
	reasonableCtx, cancel2 := context.WithTimeout(ctx, 5*time.Second)
	defer cancel2()

	if err := c.Put(reasonableCtx, []byte("fast"), []byte("data")); err != nil {
		fmt.Printf("    ✗ Put failed: %v\n", err)
	} else {
		fmt.Println("    ✓ Put succeeded with 5s timeout")
	}

	fmt.Println("\nDone!")
}
