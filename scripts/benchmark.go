package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jerkeyray/mimori/client"
)

func main() {
	var (
		addrs       = flag.String("addrs", "localhost:4000,localhost:4002,localhost:4004", "Comma-separated cluster addresses")
		duration    = flag.Duration("duration", 60*time.Second, "Benchmark duration")
		writers     = flag.Int("writers", 10, "Number of concurrent writers")
		readers     = flag.Int("readers", 10, "Number of concurrent readers")
		allowStale  = flag.Bool("allow-stale", true, "Allow stale reads from followers")
		keySize     = flag.Int("key-size", 16, "Key size in bytes")
		valueSize   = flag.Int("value-size", 128, "Value size in bytes")
	)
	flag.Parse()

	fmt.Println("=== Mimori Performance Benchmark ===")
	fmt.Printf("Cluster: %s\n", *addrs)
	fmt.Printf("Duration: %v\n", *duration)
	fmt.Printf("Writers: %d\n", *writers)
	fmt.Printf("Readers: %d\n", *readers)
	fmt.Printf("Allow Stale Reads: %v\n", *allowStale)
	fmt.Printf("Key Size: %d bytes\n", *keySize)
	fmt.Printf("Value Size: %d bytes\n\n", *valueSize)

	// Parse cluster addresses
	clusterAddrs := parseAddrs(*addrs)
	if len(clusterAddrs) == 0 {
		fmt.Fprintf(os.Stderr, "Error: no cluster addresses provided\n")
		os.Exit(1)
	}

	// Create client
	c, err := client.New(clusterAddrs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating client: %v\n", err)
		os.Exit(1)
	}
	defer c.Close()

	// Test connectivity
	fmt.Println("Testing connectivity...")
	if err := c.Health(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "Error: cluster not reachable: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✓ Cluster is reachable\n")

	// Generate test data
	keyTemplate := make([]byte, *keySize)
	valueTemplate := make([]byte, *valueSize)
	for i := range keyTemplate {
		keyTemplate[i] = 'k'
	}
	for i := range valueTemplate {
		valueTemplate[i] = 'v'
	}

	// Counters
	var (
		writesCompleted int64
		readsCompleted  int64
		writeErrors     int64
		readErrors      int64

		writeLatencies []time.Duration
		readLatencies  []time.Duration
		latencyMu      sync.Mutex
	)

	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()

	var wg sync.WaitGroup
	startTime := time.Now()

	// Writer goroutines
	for i := 0; i < *writers; i++ {
		wg.Add(1)
		go func(writerID int) {
			defer wg.Done()
			keyCounter := 0
			for {
				select {
				case <-ctx.Done():
					return
				default:
					keyCounter++
					key := []byte(fmt.Sprintf("bench-w%d-k%d", writerID, keyCounter))
					value := []byte(fmt.Sprintf("value-%d-%d-%s", writerID, keyCounter, valueTemplate[:min(100, len(valueTemplate))]))

					start := time.Now()
					err := c.Put(context.Background(), key, value)
					latency := time.Since(start)

					if err != nil {
						atomic.AddInt64(&writeErrors, 1)
					} else {
						atomic.AddInt64(&writesCompleted, 1)

						// Sample latencies (10% sampling to avoid memory issues)
						if keyCounter%10 == 0 {
							latencyMu.Lock()
							writeLatencies = append(writeLatencies, latency)
							latencyMu.Unlock()
						}
					}
				}
			}
		}(i)
	}

	// Reader goroutines
	for i := 0; i < *readers; i++ {
		wg.Add(1)
		go func(readerID int) {
			defer wg.Done()
			keyCounter := 0
			for {
				select {
				case <-ctx.Done():
					return
				default:
					keyCounter++
					// Read keys written by all writers (round-robin)
					writerID := keyCounter % *writers
					keyNum := keyCounter / *writers
					key := []byte(fmt.Sprintf("bench-w%d-k%d", writerID, keyNum))

					start := time.Now()
					var err error
					if *allowStale {
						_, _, err = c.GetWithOptions(context.Background(), key, client.GetOptions{AllowStale: true})
					} else {
						_, _, err = c.Get(context.Background(), key)
					}
					latency := time.Since(start)

					if err != nil {
						atomic.AddInt64(&readErrors, 1)
					} else {
						atomic.AddInt64(&readsCompleted, 1)

						// Sample latencies (10% sampling)
						if keyCounter%10 == 0 {
							latencyMu.Lock()
							readLatencies = append(readLatencies, latency)
							latencyMu.Unlock()
						}
					}
				}
			}
		}(i)
	}

	// Progress reporter
	ticker := time.NewTicker(5 * time.Second)
	go func() {
		for {
			select {
			case <-ctx.Done():
				ticker.Stop()
				return
			case <-ticker.C:
				elapsed := time.Since(startTime).Seconds()
				w := atomic.LoadInt64(&writesCompleted)
				r := atomic.LoadInt64(&readsCompleted)
				fmt.Printf("[%.0fs] Writes: %d (%.0f/s) | Reads: %d (%.0f/s)\n",
					elapsed, w, float64(w)/elapsed, r, float64(r)/elapsed)
			}
		}
	}()

	// Wait for completion
	wg.Wait()

	elapsed := time.Since(startTime).Seconds()

	// Print results
	fmt.Println("\n=== Benchmark Results ===")
	fmt.Printf("Total Duration: %.2fs\n\n", elapsed)

	totalWrites := atomic.LoadInt64(&writesCompleted)
	totalReads := atomic.LoadInt64(&readsCompleted)
	writeErrs := atomic.LoadInt64(&writeErrors)
	readErrs := atomic.LoadInt64(&readErrors)

	fmt.Println("Writes:")
	fmt.Printf("  Total: %d\n", totalWrites)
	fmt.Printf("  Errors: %d (%.2f%%)\n", writeErrs, float64(writeErrs)/float64(totalWrites+writeErrs)*100)
	fmt.Printf("  Throughput: %.2f ops/sec\n", float64(totalWrites)/elapsed)
	if len(writeLatencies) > 0 {
		fmt.Printf("  Latency (p50): %.2fms\n", percentile(writeLatencies, 0.50).Seconds()*1000)
		fmt.Printf("  Latency (p95): %.2fms\n", percentile(writeLatencies, 0.95).Seconds()*1000)
		fmt.Printf("  Latency (p99): %.2fms\n", percentile(writeLatencies, 0.99).Seconds()*1000)
	}

	fmt.Println("\nReads:")
	fmt.Printf("  Total: %d\n", totalReads)
	fmt.Printf("  Errors: %d (%.2f%%)\n", readErrs, float64(readErrs)/float64(totalReads+readErrs)*100)
	fmt.Printf("  Throughput: %.2f ops/sec\n", float64(totalReads)/elapsed)
	if len(readLatencies) > 0 {
		fmt.Printf("  Latency (p50): %.2fms\n", percentile(readLatencies, 0.50).Seconds()*1000)
		fmt.Printf("  Latency (p95): %.2fms\n", percentile(readLatencies, 0.95).Seconds()*1000)
		fmt.Printf("  Latency (p99): %.2fms\n", percentile(readLatencies, 0.99).Seconds()*1000)
	}

	fmt.Println("\nOverall:")
	fmt.Printf("  Total Operations: %d\n", totalWrites+totalReads)
	fmt.Printf("  Overall Throughput: %.2f ops/sec\n", float64(totalWrites+totalReads)/elapsed)
}

func parseAddrs(addrs string) []string {
	if addrs == "" {
		return nil
	}
	var result []string
	for _, addr := range splitComma(addrs) {
		if addr != "" {
			result = append(result, addr)
		}
	}
	return result
}

func splitComma(s string) []string {
	var result []string
	current := ""
	for _, c := range s {
		if c == ',' {
			result = append(result, current)
			current = ""
		} else {
			current += string(c)
		}
	}
	if current != "" {
		result = append(result, current)
	}
	return result
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// percentile calculates the p-th percentile of a duration slice (p in [0, 1])
func percentile(durations []time.Duration, p float64) time.Duration {
	if len(durations) == 0 {
		return 0
	}

	// Simple bubble sort (ok for sampled data)
	sorted := make([]time.Duration, len(durations))
	copy(sorted, durations)
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[i] > sorted[j] {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	idx := int(float64(len(sorted)-1) * p)
	return sorted[idx]
}
