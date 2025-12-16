package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/jerkeyray/mimori/internal/api/kv"
	raftpb "github.com/jerkeyray/mimori/internal/raft/raftpb"
)

// notes:
// mimorictl acts as a gRPC client
// cli commands get translated into a protocol buffer message
// and are sent over the network to a specific node by --addr flag

// Server address for the node (can be overridden by flag or env)
var addr string

// Default timeout for requests
const timeout = 3 * time.Second

// ENTRY POINT
func main() {
	rootCmd := &cobra.Command{
		Use:   "mimorictl",
		Short: "MimoriDB CLI — talk to a running Mimori node",
		Long: `mimorictl is a simple client for sending key/value commands
to a MimoriDB node running locally or remotely.`,
	}

	// Global flag to specify which node to talk to
	rootCmd.PersistentFlags().StringVar(
		&addr,
		"addr",
		"127.0.0.1:4000,127.0.0.1:4002,127.0.0.1:4004",
		"comma-separated gRPC addresses to seed discovery (e.g. 127.0.0.1:4002,127.0.0.1:4000)",
	)

	// Add subcommands
	rootCmd.AddCommand(
		newPutCmd(),
		newGetCmd(),
		newDelCmd(),
		newHealthCmd(),
		newStatusCmd(),
		newSnapshotCmd(),
		newMetricsCmd(),
		newLeaderCmd(),
		newAddNodeCmd(),
		newRemoveNodeCmd(),
		newTransferLeadershipCmd(),
	)

	if err := rootCmd.Execute(); err != nil {
		log.Fatalf("command error: %v", err)
	}
}

// COMMAND DEFINITIONS

// newPutCmd creates the "put" subcommand: mimorictl put key value
func newPutCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "put [key] [value]",
		Aliases: []string{"p"},
		Short:   "Store a key/value pair in the database",
		Args:    cobra.ExactArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			key := []byte(args[0])
			val := []byte(args[1])

			if err := doPut(key, val); err != nil {
				log.Fatalf("put failed: %v", err)
			}

			fmt.Println("ok")
		},
	}
}

// newGetCmd creates "get" subcommand: mimorictl get key
func newGetCmd() *cobra.Command {
	var allowStale bool
	cmd := &cobra.Command{
		Use:     "get [key]",
		Aliases: []string{"g"},
		Short:   "Fetch a value for a key",
		Long: `Fetch a value for a key from the cluster.
By default, reads go to the leader for strong consistency.
Use --allow-stale to allow reads from followers (may return stale data).`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			key := []byte(args[0])
			cm := getClientManager()

			var resp *kv.GetResponse
			err := cm.executeWithRetry(func(leaderAddr string) error {
				client, err := cm.getKVClient(leaderAddr)
				if err != nil {
					return err
				}

				ctx, cancel := context.WithTimeout(context.Background(), timeout)
				defer cancel()

				resp, err = client.Get(ctx, &kv.GetRequest{
					Key:        key,
					AllowStale: allowStale,
				})
				return err
			}, 3)

			if err != nil {
				log.Fatalf("get failed: %v", err)
			}

			if !resp.Found {
				fmt.Println("(nil)")
				return
			}
			fmt.Printf("%s\n", string(resp.Value))
		},
	}
	cmd.Flags().BoolVar(&allowStale, "allow-stale", false, "Allow reads from followers (may return stale data)")
	return cmd
}

// newDelCmd creates "del" subcommand: mimorictl del key
func newDelCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "del [key]",
		Aliases: []string{"d"},
		Args:    cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			key := []byte(args[0])

			if err := doDelete(key); err != nil {
				log.Fatalf("delete failed: %v", err)
			}

			fmt.Println("deleted")
		},
	}
}

// newHealthCmd creates "health" subcommand: mimorictl health
func newHealthCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "health",
		Aliases: []string{"h"},
		Short:   "Check if the node is alive",
		Args:    cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			cm := getClientManager()
			leaderAddr, err := cm.getLeader()
			if err != nil {
				// Try initial address if leader discovery fails
				leaderAddr = addr
			}

			client, err := cm.getKVClient(leaderAddr)
			if err != nil {
				log.Fatalf("health check failed: %v", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()

			resp, err := client.Health(ctx, &kv.HealthRequest{})
			if err != nil {
				log.Fatalf("health check failed: %v", err)
			}
			fmt.Println(resp.Status)
		},
	}
}

// HELPER FUNCTIONS

// clientWrapper wraps a gRPC client connection and the generated Mimori service client.
type clientWrapper struct {
	Client kv.KVClient      // the remote control
	conn   *grpc.ClientConn // the connection cable
}

// Close releases the underlying gRPC connection resources.
func (cw *clientWrapper) Close() {
	if cw.conn != nil {
		_ = cw.conn.Close()
	}
}

func mustConnect() *clientWrapper {
	// set a connection timeout of 3 seconds
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// dial the server
	conn, err := grpc.DialContext(
		ctx,
		addr, // from the global flag
		grpc.WithTransportCredentials(insecure.NewCredentials()), // tells gRPC to not use TLS/SSL (for now)
	)
	if err != nil {
		log.Fatalf("failed to connect to node at %s: %v", addr, err)
	}

	// create service client and return the wrapper
	client := kv.NewKVClient(conn)
	return &clientWrapper{Client: client, conn: conn}
}

// try to pull "leader=ADDR" out of the error message
func extractLeaderAddr(err error) (string, bool) {
	msg := err.Error()
	// server formats as: "not leader, leader=:4001"
	idx := strings.Index(msg, "leader=")
	if idx == -1 {
		return "", false
	}

	leader := msg[idx+len("leader="):]
	leader = strings.TrimSpace(leader)
	if leader == "" {
		return "", false
	}
	return leader, true
}

func doPut(key, val []byte) error {
	cm := getClientManager()
	return cm.executeWithRetry(func(leaderAddr string) error {
		client, err := cm.getKVClient(leaderAddr)
		if err != nil {
			return err
		}

		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		_, err = client.Put(ctx, &kv.PutRequest{Key: key, Value: val})
		return err
	}, 3)
}

func doDelete(key []byte) error {
	cm := getClientManager()
	return cm.executeWithRetry(func(leaderAddr string) error {
		client, err := cm.getKVClient(leaderAddr)
		if err != nil {
			return err
		}

		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		_, err = client.Delete(ctx, &kv.DeleteRequest{Key: key})
		return err
	}, 3)
}

// same as mustConnect but allows choosing a different target address
func mustConnectTo(a string) *clientWrapper {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	conn, err := grpc.DialContext(
		ctx,
		a,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)

	if err != nil {
		log.Fatalf("failed to connect to node at %s: %v", a, err)
	}

	client := kv.NewKVClient(conn)
	return &clientWrapper{Client: client, conn: conn}
}

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "status",
		Aliases: []string{"st"},
		Short:   "Show Raft and node status",
		Long:    "Display detailed Raft status including node ID, state, term, commit index, and log length",
		Args:    cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			body := mustHTTPGetFromSeeds("/raft/status", 2*time.Second)

			// Pretty print JSON
			var status map[string]interface{}
			if err := json.Unmarshal(body, &status); err != nil {
				fmt.Println(string(body))
				return
			}

			fmt.Printf("Node ID:      %v\n", status["id"])
			fmt.Printf("State:        %v\n", status["state"])
			fmt.Printf("Term:         %v\n", status["term"])
			fmt.Printf("Leader ID:    %v\n", status["leader_id"])
			fmt.Printf("Commit Index: %v\n", status["commit_index"])
			fmt.Printf("Last Applied: %v\n", status["last_applied"])
			fmt.Printf("Log Length:   %v\n", status["log_length"])
		},
	}
}

func newSnapshotCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "snapshot",
		Aliases: []string{"snap"},
		Short:   "Force creation of a snapshot",
		Long:    "Trigger snapshot creation on the leader node. This compacts the log and creates a checkpoint.",
		Args:    cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			leaderGRPC := mustDiscoverLeaderFromSeeds()
			leaderHTTP := getHTTPAddr(leaderGRPC)
			url := fmt.Sprintf("http://%s/raft/snapshot", leaderHTTP)

			client := http.Client{Timeout: 5 * time.Second}
			req, _ := http.NewRequest(http.MethodPost, url, nil)
			resp, err := client.Do(req)
			if err != nil {
				log.Fatalf("failed to create snapshot: %v", err)
			}
			defer resp.Body.Close()

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				log.Fatalf("failed to read response: %v", err)
			}

			if resp.StatusCode == http.StatusPreconditionFailed {
				var result map[string]interface{}
				json.Unmarshal(body, &result)
				log.Fatalf("snapshot failed: %v (leader is %v)", result["error"], result["leader_id"])
			}

			if resp.StatusCode != http.StatusOK {
				log.Fatalf("snapshot failed: %s", string(body))
			}
			fmt.Println("Snapshot created successfully")
		},
	}
}

func newMetricsCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "metrics",
		Aliases: []string{"m"},
		Short:   "Show key Prometheus metrics",
		Long:    "Display important Raft metrics in a readable format",
		Args:    cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			body := mustHTTPGetFromSeeds("/metrics", 2*time.Second)

			// Parse Prometheus metrics and show key ones
			lines := strings.Split(string(body), "\n")
			keyMetrics := []string{
				"raft_node_is_leader",
				"raft_term",
				"raft_commit_index",
				"raft_last_applied_index",
				"raft_log_length",
				"raft_proposals_total",
				"raft_applied_total",
				"raft_snapshots_created_total",
			}

			fmt.Println("Key Raft Metrics:")
			fmt.Println("==================")
			for _, line := range lines {
				for _, metric := range keyMetrics {
					if strings.HasPrefix(line, metric) && !strings.HasPrefix(line, "#") {
						fmt.Println(line)
						break
					}
				}
			}
		},
	}
}

func newLeaderCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "leader",
		Aliases: []string{"ldr"},
		Short:   "Show current leader information",
		Args:    cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			body := mustHTTPGetFromSeeds("/raft/status", 2*time.Second)
			var status map[string]interface{}
			if err := json.Unmarshal(body, &status); err != nil {
				log.Fatalf("failed to parse status: %v", err)
			}

			leaderID := status["leader_id"]
			if leaderID == "" {
				fmt.Println("No leader elected")
				return
			}

			isLeader := status["state"] == "leader"
			fmt.Printf("Leader ID: %v\n", leaderID)
			if isLeader {
				fmt.Printf("This node (%v) is the leader\n", status["id"])
			} else {
				fmt.Printf("This node (%v) is a follower\n", status["id"])
			}
			fmt.Printf("Term: %v\n", status["term"])
		},
	}
}

func portToInt(p string) int {
	n, err := strconv.Atoi(p)
	if err != nil {
		log.Fatalf("invalid port: %s", p)
	}
	return n
}

// Helper functions for HTTP endpoints
func seedAddrs() []string {
	// Prefer env-provided seeds (same precedence as client.go)
	raw := addr
	if v := os.Getenv("MIMORI_ADDRS"); v != "" {
		raw = v
	}
	if v := os.Getenv("MIMORI_SEEDS"); v != "" {
		raw = v
	}
	seeds := splitAddrList(raw)
	if len(seeds) == 0 && strings.TrimSpace(raw) != "" {
		return []string{strings.TrimSpace(raw)}
	}
	return seeds
}

func mustHTTPGetFromSeeds(path string, timeout time.Duration) []byte {
	seeds := seedAddrs()
	if len(seeds) == 0 {
		log.Fatalf("no --addr provided")
	}

	client := http.Client{Timeout: timeout}
	var lastErr error
	for _, seed := range seeds {
		httpAddr := getHTTPAddr(seed)
		url := fmt.Sprintf("http://%s%s", httpAddr, path)
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		b, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("http %d: %s", resp.StatusCode, string(b))
			continue
		}
		return b
	}
	log.Fatalf("failed to fetch %s from any seed: %v", path, lastErr)
	return nil
}

func mustDiscoverLeaderFromSeeds() string {
	seeds := seedAddrs()
	if len(seeds) == 0 {
		log.Fatalf("no --addr provided")
	}

	type raftStatus struct {
		State    string `json:"state"`
		LeaderID string `json:"leader_id"`
	}

	client := http.Client{Timeout: 2 * time.Second}
	var lastErr error
	for _, seed := range seeds {
		httpAddr := getHTTPAddr(seed)
		url := fmt.Sprintf("http://%s/raft/status", httpAddr)
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("http %d: %s", resp.StatusCode, string(body))
			continue
		}

		var st raftStatus
		if err := json.Unmarshal(body, &st); err != nil {
			lastErr = err
			continue
		}
		if strings.EqualFold(st.State, "leader") {
			return seed
		}
		if st.LeaderID != "" {
			return normalizeLeaderAddr(seed, st.LeaderID)
		}
		lastErr = fmt.Errorf("seed %s does not know leader", seed)
	}
	log.Fatalf("failed to discover leader from any seed: %v", lastErr)
	return ""
}

func newAddNodeCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "add-node [node-id]",
		Aliases: []string{"add"},
		Short:   "Add a node to the cluster",
		Long:    "Add a node to the cluster. The node-id should be the address (e.g., :4000) that the new node will use. This command must be run against the leader.",
		Args:    cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			nodeID := args[0]
			if err := doAddNode(nodeID); err != nil {
				log.Fatalf("add-node failed: %v", err)
			}
			fmt.Printf("Node %s added to cluster successfully\n", nodeID)
		},
	}
}

func newRemoveNodeCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "remove-node [node-id]",
		Aliases: []string{"rm"},
		Short:   "Remove a node from the cluster",
		Long:    "Remove a node from the cluster. This command must be run against the leader. Cannot remove the last remaining peer.",
		Args:    cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			nodeID := args[0]
			if err := doRemoveNode(nodeID); err != nil {
				log.Fatalf("remove-node failed: %v", err)
			}
			fmt.Printf("Node %s removed from cluster successfully\n", nodeID)
		},
	}
}

func doAddNode(nodeID string) error {
	cm := getClientManager()
	configTimeout := 10 * time.Second

	return cm.executeWithRetry(func(leaderAddr string) error {
		raftClient, err := cm.getRaftClient(leaderAddr)
		if err != nil {
			return err
		}

		ctx, cancel := context.WithTimeout(context.Background(), configTimeout)
		defer cancel()

		resp, err := raftClient.AddNode(ctx, &raftpb.AddNodeRequest{NodeId: nodeID})
		if err != nil {
			return err
		}

		if !resp.Success {
			return fmt.Errorf("%s", resp.Error)
		}

		return nil
	}, 3)
}

func doRemoveNode(nodeID string) error {
	cm := getClientManager()
	configTimeout := 10 * time.Second

	return cm.executeWithRetry(func(leaderAddr string) error {
		raftClient, err := cm.getRaftClient(leaderAddr)
		if err != nil {
			return err
		}

		ctx, cancel := context.WithTimeout(context.Background(), configTimeout)
		defer cancel()

		resp, err := raftClient.RemoveNode(ctx, &raftpb.RemoveNodeRequest{NodeId: nodeID})
		if err != nil {
			return err
		}

		if !resp.Success {
			return fmt.Errorf("%s", resp.Error)
		}

		return nil
	}, 3)
}

func newTransferLeadershipCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "transfer-leadership [target-node-id]",
		Aliases: []string{"tl"},
		Short:   "Transfer leadership to another node",
		Long:    "Gracefully transfer leadership to the specified node. This is useful for maintenance - transfer leadership before shutting down the current leader. The target node must be caught up with the current leader.",
		Args:    cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			targetNodeID := args[0]
			if err := doTransferLeadership(targetNodeID); err != nil {
				log.Fatalf("transfer-leadership failed: %v", err)
			}
			fmt.Printf("Leadership transfer initiated to %s\n", targetNodeID)
			fmt.Println("The current leader will step down once the target node becomes leader.")
		},
	}
}

func doTransferLeadership(targetNodeID string) error {
	cm := getClientManager()
	transferTimeout := 15 * time.Second

	return cm.executeWithRetry(func(leaderAddr string) error {
		raftClient, err := cm.getRaftClient(leaderAddr)
		if err != nil {
			return err
		}

		ctx, cancel := context.WithTimeout(context.Background(), transferTimeout)
		defer cancel()

		resp, err := raftClient.TransferLeadership(ctx, &raftpb.TransferLeadershipRequest{
			TargetNodeId: targetNodeID,
		})
		if err != nil {
			return err
		}

		if !resp.Success {
			return fmt.Errorf("%s", resp.Error)
		}

		return nil
	}, 3)
}
