/*
Package client provides a production-ready Go client library for Mimori distributed key-value store.

The client automatically handles:
  - Leader discovery via seed nodes
  - Connection pooling and reuse
  - Automatic retries with exponential backoff
  - Graceful error handling

# Basic Usage

	import "github.com/jerkeyray/mimori/client"

	// Create a client
	c, err := client.New([]string{"localhost:4000", "localhost:4002", "localhost:4004"})
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	ctx := context.Background()

	// Put a key-value pair
	err = c.Put(ctx, []byte("key"), []byte("value"))

	// Get a value (strong consistency, reads from leader)
	value, found, err := c.Get(ctx, []byte("key"))

	// Get with stale reads allowed (reads from followers)
	value, found, err = c.GetWithOptions(ctx, []byte("key"), client.GetOptions{
		AllowStale: true,
	})

	// Delete a key
	err = c.Delete(ctx, []byte("key"))

# Configuration

For custom timeouts and retry behavior, use NewWithConfig:

	cfg := client.Config{
		Seeds:       []string{"localhost:4000"},
		ConnTimeout: 5 * time.Second,
		ReqTimeout:  10 * time.Second,
		MaxRetries:  5,
	}
	c, err := client.NewWithConfig(cfg)

# Context Support

All operations accept a context.Context for cancellation and timeouts:

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := c.Put(ctx, key, value)

# Thread Safety

The Client type is safe for concurrent use by multiple goroutines.
*/
package client
