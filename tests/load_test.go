package tests

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jerkeyray/mimori/internal/api/kv"
)

// TestLoad_HighThroughput tests the cluster under high write load.
func TestLoad_HighThroughput(t *testing.T) {
	rand.Seed(time.Now().UnixNano())
	cluster := NewMiniCluster()
	ids := []string{"node1", "node2", "node3"}

	// Start all nodes
	for _, id := range ids {
		others := []string{}
		for _, o := range ids {
			if o != id {
				others = append(others, o)
			}
		}
		cluster.StartNode(t, id, others)
	}
	defer func() {
		for _, id := range ids {
			cluster.StopNode(id)
		}
	}()

	// Wait for leader
	leader := waitForLeader(t, cluster, ids, 10*time.Second)
	if leader == nil {
		t.Fatal("no leader found")
	}

	// Load test parameters
	duration := 10 * time.Second
	numWriters := 10
	numReaders := 5

	var writesCompleted int64
	var readsCompleted int64
	var writeErrors int64
	var readErrors int64

	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	var wg sync.WaitGroup

	// Writer goroutines
	for i := 0; i < numWriters; i++ {
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
					key := []byte(fmt.Sprintf("load-w%d-k%d", writerID, keyCounter))
					value := []byte(fmt.Sprintf("value-%d-%d", writerID, keyCounter))

					currentLeader := findLeader(cluster, ids)
					if currentLeader == nil {
						atomic.AddInt64(&writeErrors, 1)
						time.Sleep(50 * time.Millisecond)
						continue
					}

					_, err := currentLeader.Client.Put(context.Background(), &kv.PutRequest{
						Key:   key,
						Value: value,
					})
					if err != nil {
						atomic.AddInt64(&writeErrors, 1)
					} else {
						atomic.AddInt64(&writesCompleted, 1)
					}

					// Small delay to avoid overwhelming
					time.Sleep(10 * time.Millisecond)
				}
			}
		}(i)
	}

	// Reader goroutines (read with allow_stale for follower reads)
	for i := 0; i < numReaders; i++ {
		wg.Add(1)
		go func(readerID int) {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
					// Read a random key
					writerID := rand.Intn(numWriters)
					keyNum := rand.Intn(100) + 1
					key := []byte(fmt.Sprintf("load-w%d-k%d", writerID, keyNum))

					// Try any node (follower reads)
					aliveNodes := getAliveNodes(cluster, ids)
					if len(aliveNodes) == 0 {
						atomic.AddInt64(&readErrors, 1)
						time.Sleep(50 * time.Millisecond)
						continue
					}

					nodeID := aliveNodes[rand.Intn(len(aliveNodes))]
					node := cluster.GetNode(nodeID)
					if node == nil {
						atomic.AddInt64(&readErrors, 1)
						continue
					}

					_, err := node.Client.Get(context.Background(), &kv.GetRequest{
						Key:        key,
						AllowStale: true,
					})
					if err != nil {
						atomic.AddInt64(&readErrors, 1)
					} else {
						atomic.AddInt64(&readsCompleted, 1)
					}

					time.Sleep(20 * time.Millisecond)
				}
			}
		}(i)
	}

	// Wait for load test to complete
	wg.Wait()

	// Report results
	totalWrites := atomic.LoadInt64(&writesCompleted)
	totalReads := atomic.LoadInt64(&readsCompleted)
	writeErrs := atomic.LoadInt64(&writeErrors)
	readErrs := atomic.LoadInt64(&readErrors)

	t.Logf("Load Test Results:")
	t.Logf("  Duration: %v", duration)
	t.Logf("  Writers: %d, Readers: %d", numWriters, numReaders)
	t.Logf("  Writes: %d completed, %d errors", totalWrites, writeErrs)
	t.Logf("  Reads: %d completed, %d errors", totalReads, readErrs)
	t.Logf("  Write throughput: %.2f ops/sec", float64(totalWrites)/duration.Seconds())
	t.Logf("  Read throughput: %.2f ops/sec", float64(totalReads)/duration.Seconds())
	t.Logf("  Total throughput: %.2f ops/sec", float64(totalWrites+totalReads)/duration.Seconds())

	// Verify final state: cluster should still be healthy
	time.Sleep(1 * time.Second)
	finalLeader := waitForLeader(t, cluster, ids, 5*time.Second)
	if finalLeader == nil {
		t.Fatal("no leader after load test")
	}

	t.Logf("Cluster remained healthy after load test")
}

// TestLoad_BurstWrite tests handling of burst write operations.
func TestLoad_BurstWrite(t *testing.T) {
	rand.Seed(time.Now().UnixNano())
	cluster := NewMiniCluster()
	ids := []string{"node1", "node2", "node3"}

	// Start all nodes
	for _, id := range ids {
		others := []string{}
		for _, o := range ids {
			if o != id {
				others = append(others, o)
			}
		}
		cluster.StartNode(t, id, others)
	}
	defer func() {
		for _, id := range ids {
			cluster.StopNode(id)
		}
	}()

	// Wait for leader
	leader := waitForLeader(t, cluster, ids, 10*time.Second)
	if leader == nil {
		t.Fatal("no leader found")
	}

	// Burst write: send many writes quickly
	burstSize := 100
	var successCount int64
	var errorCount int64

	t.Logf("Starting burst write of %d operations...", burstSize)

	var wg sync.WaitGroup
	for i := 0; i < burstSize; i++ {
		wg.Add(1)
		go func(keyNum int) {
			defer wg.Done()
			key := []byte(fmt.Sprintf("burst-key-%d", keyNum))
			value := []byte(fmt.Sprintf("burst-value-%d", keyNum))

			currentLeader := findLeader(cluster, ids)
			if currentLeader == nil {
				atomic.AddInt64(&errorCount, 1)
				return
			}

			_, err := currentLeader.Client.Put(context.Background(), &kv.PutRequest{
				Key:   key,
				Value: value,
			})
			if err != nil {
				atomic.AddInt64(&errorCount, 1)
			} else {
				atomic.AddInt64(&successCount, 1)
			}
		}(i)
	}

	wg.Wait()

	success := atomic.LoadInt64(&successCount)
	errors := atomic.LoadInt64(&errorCount)

	t.Logf("Burst write completed: %d successful, %d errors", success, errors)

	// Verify all writes were replicated
	time.Sleep(2 * time.Second)

	verified := 0
	for i := 0; i < burstSize; i++ {
		key := []byte(fmt.Sprintf("burst-key-%d", i))

		// Try to read from any node
		for _, id := range ids {
			node := cluster.GetNode(id)
			if node == nil {
				continue
			}

			val, found, err := node.Store.Get(key)
			if err == nil && found {
				expectedValue := fmt.Sprintf("burst-value-%d", i)
				if string(val) == expectedValue {
					verified++
					break
				}
			}
		}
	}

	t.Logf("Verified %d/%d writes were replicated", verified, burstSize)

	if verified < burstSize*9/10 { // At least 90% should be replicated
		t.Errorf("Too few writes verified: %d/%d", verified, burstSize)
	}
}

// TestLoad_ConcurrentOperations tests concurrent read/write operations.
func TestLoad_ConcurrentOperations(t *testing.T) {
	rand.Seed(time.Now().UnixNano())
	cluster := NewMiniCluster()
	ids := []string{"node1", "node2", "node3"}

	// Start all nodes
	for _, id := range ids {
		others := []string{}
		for _, o := range ids {
			if o != id {
				others = append(others, o)
			}
		}
		cluster.StartNode(t, id, others)
	}
	defer func() {
		for _, id := range ids {
			cluster.StopNode(id)
		}
	}()

	// Wait for leader
	leader := waitForLeader(t, cluster, ids, 10*time.Second)
	if leader == nil {
		t.Fatal("no leader found")
	}

	// Write initial data
	numKeys := 50
	for i := 0; i < numKeys; i++ {
		key := []byte(fmt.Sprintf("concurrent-key-%d", i))
		value := []byte(fmt.Sprintf("initial-value-%d", i))
		_, err := leader.Client.Put(context.Background(), &kv.PutRequest{
			Key:   key,
			Value: value,
		})
		if err != nil {
			t.Fatalf("Failed to write initial data: %v", err)
		}
	}

	// Wait for replication
	time.Sleep(1 * time.Second)

	// Concurrent operations
	var updates int64
	var reads int64
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup

	// Updater: update values concurrently
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			default:
				keyNum := rand.Intn(numKeys)
				key := []byte(fmt.Sprintf("concurrent-key-%d", keyNum))
				value := []byte(fmt.Sprintf("updated-value-%d-%d", keyNum, time.Now().UnixNano()))

				currentLeader := findLeader(cluster, ids)
				if currentLeader == nil {
					continue
				}

				_, err := currentLeader.Client.Put(context.Background(), &kv.PutRequest{
					Key:   key,
					Value: value,
				})
				if err == nil {
					atomic.AddInt64(&updates, 1)
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
	}()

	// Readers: read from all nodes (follower reads)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
					keyNum := rand.Intn(numKeys)
					key := []byte(fmt.Sprintf("concurrent-key-%d", keyNum))

					// Read from random node
					aliveNodes := getAliveNodes(cluster, ids)
					if len(aliveNodes) == 0 {
						continue
					}

					nodeID := aliveNodes[rand.Intn(len(aliveNodes))]
					node := cluster.GetNode(nodeID)
					if node == nil {
						continue
					}

					_, err := node.Client.Get(context.Background(), &kv.GetRequest{
						Key:        key,
						AllowStale: true,
					})
					if err == nil {
						atomic.AddInt64(&reads, 1)
					}
					time.Sleep(30 * time.Millisecond)
				}
			}
		}()
	}

	wg.Wait()

	t.Logf("Concurrent operations: %d updates, %d reads", atomic.LoadInt64(&updates), atomic.LoadInt64(&reads))

	// Verify final consistency
	finalLeader := waitForLeader(t, cluster, ids, 5*time.Second)
	if finalLeader == nil {
		t.Fatal("no leader after concurrent operations")
	}

	// Verify we can still read data
	for i := 0; i < 10; i++ {
		keyNum := rand.Intn(numKeys)
		key := []byte(fmt.Sprintf("concurrent-key-%d", keyNum))

		resp, err := finalLeader.Client.Get(context.Background(), &kv.GetRequest{
			Key: key,
		})
		if err != nil {
			t.Errorf("Failed to read key %s after concurrent operations: %v", key, err)
		} else if !resp.Found {
			t.Errorf("Key %s not found after concurrent operations", key)
		}
	}

	t.Log("Concurrent operations test completed successfully")
}
