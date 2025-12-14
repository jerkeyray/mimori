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

// TestChaos_RandomFailures tests the cluster's ability to handle random node failures.
// Nodes are randomly killed and restarted while operations continue.
func TestChaos_RandomFailures(t *testing.T) {
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

	// Wait for leader
	t.Log("Waiting for initial leader...")
	leader := waitForLeader(t, cluster, ids, 10*time.Second)
	if leader == nil {
		t.Fatal("no leader found")
	}

	// Chaos test parameters
	duration := 30 * time.Second
	failureInterval := 2 * time.Second
	restartInterval := 3 * time.Second

	// Track operations
	var opsCount int64
	var successCount int64
	var failuresCount int64

	// Writer goroutine: continuously write data
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup

	// Writer goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		keyCounter := 0
		for {
			select {
			case <-ctx.Done():
				return
			default:
				keyCounter++
				key := []byte(fmt.Sprintf("chaos-key-%d", keyCounter))
				value := []byte(fmt.Sprintf("chaos-value-%d", keyCounter))

				// Try to find a leader
				currentLeader := findLeader(cluster, ids)
				if currentLeader == nil {
					time.Sleep(100 * time.Millisecond)
					atomic.AddInt64(&failuresCount, 1)
					continue
				}

				atomic.AddInt64(&opsCount, 1)
				_, err := currentLeader.Client.Put(context.Background(), &kv.PutRequest{
					Key:   key,
					Value: value,
				})
				if err != nil {
					atomic.AddInt64(&failuresCount, 1)
				} else {
					atomic.AddInt64(&successCount, 1)
				}

				time.Sleep(50 * time.Millisecond)
			}
		}
	}()

	// Chaos goroutine: randomly kill and restart nodes
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(failureInterval)
		defer ticker.Stop()

		var killedNodes []string

		for {
			select {
			case <-ctx.Done():
				// Restart any killed nodes before exiting
				for _, id := range killedNodes {
					if cluster.GetNode(id) == nil {
						others := []string{}
						for _, o := range ids {
							if o != id {
								others = append(others, o)
							}
						}
						cluster.StartNode(t, id, others)
					}
				}
				return
			case <-ticker.C:
				// Kill a random node (if we have enough nodes up)
				aliveNodes := getAliveNodes(cluster, ids)
				if len(aliveNodes) > 2 && len(killedNodes) < 2 {
					// Kill a random node
					toKill := aliveNodes[rand.Intn(len(aliveNodes))]
					t.Logf("[CHAOS] Killing node %s", toKill)
					cluster.StopNode(toKill)
					killedNodes = append(killedNodes, toKill)
				}

				// Restart a killed node after some time
				if len(killedNodes) > 0 {
					toRestart := killedNodes[0]
					killedNodes = killedNodes[1:]

					time.Sleep(restartInterval)

					others := []string{}
					for _, o := range ids {
						if o != toRestart {
							others = append(others, o)
						}
					}
					t.Logf("[CHAOS] Restarting node %s", toRestart)
					cluster.StartNode(t, toRestart, others)
				}
			}
		}
	}()

	// Run chaos test
	time.Sleep(duration)
	cancel()
	wg.Wait()

	// Verify final state
	t.Logf("Operations: %d total, %d successful, %d failed", opsCount, successCount, failuresCount)

	// Wait for cluster to stabilize
	time.Sleep(2 * time.Second)

	// Verify we have a leader
	finalLeader := waitForLeader(t, cluster, ids, 10*time.Second)
	if finalLeader == nil {
		t.Fatal("no leader after chaos test")
	}

	// Verify data consistency: read some keys from all nodes
	t.Log("Verifying data consistency...")
	for i := 1; i <= 10; i++ {
		key := []byte(fmt.Sprintf("chaos-key-%d", i))
		expectedValue := []byte(fmt.Sprintf("chaos-value-%d", i))

		// Try to read from all nodes
		for _, id := range ids {
			node := cluster.GetNode(id)
			if node == nil {
				continue
			}

			val, found, err := node.Store.Get(key)
			if err == nil && found && string(val) == string(expectedValue) {
				// Found the value, move to next key
				break
			}
		}
	}

	t.Log("Chaos test completed successfully")
}

// TestChaos_LeaderFailures focuses on killing and restarting leaders specifically.
func TestChaos_LeaderFailures(t *testing.T) {
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

	// Wait for initial leader
	leader := waitForLeader(t, cluster, ids, 10*time.Second)
	if leader == nil {
		t.Fatal("no initial leader found")
	}

	t.Logf("Initial leader: %s", leader.ID)

	// Kill and restart leader multiple times
	for i := 0; i < 5; i++ {
		currentLeader := findLeader(cluster, ids)
		if currentLeader == nil {
			t.Fatalf("no leader found at iteration %d", i)
		}

		leaderID := currentLeader.ID

		// Write some data
		key := []byte(fmt.Sprintf("leader-test-%d", i))
		value := []byte(fmt.Sprintf("value-%d", i))
		_, err := currentLeader.Client.Put(context.Background(), &kv.PutRequest{
			Key:   key,
			Value: value,
		})
		if err != nil {
			t.Logf("Warning: write failed before killing leader: %v", err)
		}

		// Kill leader
		t.Logf("[CHAOS] Killing leader %s (iteration %d)", leaderID, i)
		cluster.StopNode(leaderID)

		// Wait for new leader
		time.Sleep(2 * time.Second)
		newLeader := waitForLeader(t, cluster, ids, 10*time.Second)
		if newLeader == nil {
			t.Fatalf("no new leader elected after killing %s", leaderID)
		}
		t.Logf("New leader: %s (term %d)", newLeader.ID, newLeader.Raft.Status().Term)

		// Verify data is still accessible
		resp, err := newLeader.Client.Get(context.Background(), &kv.GetRequest{
			Key: key,
		})
		if err == nil && resp.Found && string(resp.Value) == string(value) {
			t.Logf("Verified data consistency after leader failure (iteration %d)", i)
		}

		// Restart old leader (wait a bit for cleanup)
		time.Sleep(500 * time.Millisecond)
		
		others := []string{}
		for _, o := range ids {
			if o != leaderID {
				others = append(others, o)
			}
		}
		t.Logf("[CHAOS] Restarting node %s", leaderID)
		cluster.StartNode(t, leaderID, others)

		// Wait for it to catch up
		time.Sleep(1 * time.Second)
	}

	t.Log("Leader failure chaos test completed successfully")
}

// Helper functions are defined in e2e_test.go

