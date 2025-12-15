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

// TestStress_RapidLeaderChanges tests the cluster under rapid leader changes.
// This simulates a scenario where leaders are changing frequently.
func TestStress_RapidLeaderChanges(t *testing.T) {
	rand.Seed(time.Now().UnixNano())
	cluster := NewMiniCluster()
	ids := []string{"node1", "node2", "node3", "node4", "node5"}

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

	// Wait for initial leader
	leader := waitForLeader(t, cluster, ids, 10*time.Second)
	if leader == nil {
		t.Fatal("no initial leader found")
	}

	// Write initial data
	key := []byte("stress-test")
	value := []byte("initial-value")
	_, err := leader.Client.Put(context.Background(), &kv.PutRequest{
		Key:   key,
		Value: value,
	})
	if err != nil {
		t.Fatalf("Failed to write initial data: %v", err)
	}

	// Rapid leader changes: kill and restart leader every 2 seconds
	duration := 20 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	var writesCompleted int64
	var writesFailed int64

	var wg sync.WaitGroup

	// Writer goroutine: continuously try to write
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			default:
				currentLeader := findLeader(cluster, ids)
				if currentLeader == nil {
					atomic.AddInt64(&writesFailed, 1)
					time.Sleep(100 * time.Millisecond)
					continue
				}

				writeKey := []byte(fmt.Sprintf("stress-key-%d", atomic.LoadInt64(&writesCompleted)))
				writeValue := []byte(fmt.Sprintf("value-%d", atomic.LoadInt64(&writesCompleted)))

				_, err := currentLeader.Client.Put(context.Background(), &kv.PutRequest{
					Key:   writeKey,
					Value: writeValue,
				})
				if err != nil {
					atomic.AddInt64(&writesFailed, 1)
				} else {
					atomic.AddInt64(&writesCompleted, 1)
				}
				time.Sleep(50 * time.Millisecond)
			}
		}
	}()

	// Leader killer goroutine: kill current leader every 2 seconds
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				currentLeader := findLeader(cluster, ids)
				if currentLeader == nil {
					continue
				}

				leaderID := currentLeader.ID
				t.Logf("[STRESS] Killing leader %s", leaderID)
				cluster.StopNode(leaderID)

				// Wait a bit then restart
				time.Sleep(500 * time.Millisecond)
				others := []string{}
				for _, o := range ids {
					if o != leaderID {
						others = append(others, o)
					}
				}
				t.Logf("[STRESS] Restarting node %s", leaderID)
				cluster.StartNode(t, leaderID, others)
			}
		}
	}()

	// Wait for stress test to complete
	time.Sleep(duration)
	cancel()
	wg.Wait()

	t.Logf("Stress test results: %d writes completed, %d failed", atomic.LoadInt64(&writesCompleted), atomic.LoadInt64(&writesFailed))

	// Verify cluster recovers
	time.Sleep(3 * time.Second)
	finalLeader := waitForLeader(t, cluster, ids, 10*time.Second)
	if finalLeader == nil {
		t.Fatal("no leader after stress test")
	}

	// Verify original data is still there
	resp, err := finalLeader.Client.Get(context.Background(), &kv.GetRequest{
		Key: key,
	})
	if err != nil || !resp.Found || string(resp.Value) != string(value) {
		t.Errorf("Data corruption detected: expected %s, got %v", value, resp)
	}

	t.Log("Rapid leader changes test completed successfully")
}

// TestStress_ManyConcurrentWrites tests many concurrent writers.
func TestStress_ManyConcurrentWrites(t *testing.T) {
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

	// Many concurrent writers (50 writers)
	numWriters := 50
	numWritesPerWriter := 20
	var totalWrites int64
	var totalErrors int64

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var wg sync.WaitGroup

	for i := 0; i < numWriters; i++ {
		wg.Add(1)
		go func(writerID int) {
			defer wg.Done()
			for j := 0; j < numWritesPerWriter; j++ {
				select {
				case <-ctx.Done():
					return
				default:
				}

				key := []byte(fmt.Sprintf("concurrent-w%d-k%d", writerID, j))
				value := []byte(fmt.Sprintf("value-%d-%d", writerID, j))

				currentLeader := findLeader(cluster, ids)
				if currentLeader == nil {
					atomic.AddInt64(&totalErrors, 1)
					time.Sleep(100 * time.Millisecond)
					continue
				}

				_, err := currentLeader.Client.Put(context.Background(), &kv.PutRequest{
					Key:   key,
					Value: value,
				})
				if err != nil {
					atomic.AddInt64(&totalErrors, 1)
				} else {
					atomic.AddInt64(&totalWrites, 1)
				}
			}
		}(i)
	}

	wg.Wait()

	t.Logf("Many concurrent writes: %d successful, %d errors (expected %d)",
		atomic.LoadInt64(&totalWrites),
		atomic.LoadInt64(&totalErrors),
		numWriters*numWritesPerWriter)

	// Verify cluster is still healthy
	finalLeader := waitForLeader(t, cluster, ids, 5*time.Second)
	if finalLeader == nil {
		t.Fatal("no leader after concurrent writes")
	}

	// Verify some writes were successful
	if atomic.LoadInt64(&totalWrites) < int64(numWriters*numWritesPerWriter*8/10) {
		t.Errorf("Too few successful writes: %d/%d", atomic.LoadInt64(&totalWrites), numWriters*numWritesPerWriter)
	}

	t.Log("Many concurrent writes test completed successfully")
}
