package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/jerkeyray/mimori/internal/api/kv"
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
	rootCmd.PersistentFlags().StringVar(&addr, "addr", "127.0.0.1:4000", "address of Mimori node")

	// Add subcommands
	rootCmd.AddCommand(
		newPutCmd(),
		newGetCmd(),
		newDelCmd(),
		newHealthCmd(),
		newStatusCmd(),
	)

	if err := rootCmd.Execute(); err != nil {
		log.Fatalf("command error: %v", err)
	}
}

// COMMAND DEFINITIONS

// newPutCmd creates the "put" subcommand: mimorictl put key value
func newPutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "put [key] [value]",
		Short: "Store a key/value pair in the database",
		Args:  cobra.ExactArgs(2),
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
	return &cobra.Command{
		Use:   "get [key]",
		Short: "Fetch a value for a key",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			key := []byte(args[0])
			client := mustConnect()
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()

			resp, err := client.Client.Get(ctx, &kv.GetRequest{Key: key})
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
}

// newDelCmd creates "del" subcommand: mimorictl del key
func newDelCmd() *cobra.Command {
	return &cobra.Command{
		Use:  "del [key]",
		Args: cobra.ExactArgs(1),
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
		Use:   "health",
		Short: "Check if the node is alive",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			client := mustConnect()
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()

			resp, err := client.Client.Health(ctx, &kv.HealthRequest{})
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
	addrToUse := addr // default CLI flag

	for attempt := 0; attempt < 2; attempt++ {
		client := mustConnectTo(addrToUse)
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		_, err := client.Client.Put(ctx, &kv.PutRequest{Key: key, Value: val})
		client.Close()

		if err == nil {
			return nil
		}

		// see if this is a "not leader" case
		if leader, ok := extractLeaderAddr(err); ok {
			addrToUse = leader
			continue // retry on leader
		}

		return err
	}

	return fmt.Errorf("redirected but still failed")
}

func doDelete(key []byte) error {
	addrToUse := addr

	for attempt := 0; attempt < 2; attempt++ {
		client := mustConnectTo(addrToUse)
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		_, err := client.Client.Delete(ctx, &kv.DeleteRequest{Key: key})
		client.Close()

		if err == nil {
			return nil
		}

		// check redirect
		if leader, ok := extractLeaderAddr(err); ok {
			addrToUse = leader
			continue
		}

		return err
	}

	return fmt.Errorf("redirected but still failed")
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
		Use:   "status",
		Short: "Show Raft and node status for debugging",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {

			// build HTTP URL: if grpc is :4000, health/status is :4001
			host, port, ok := strings.Cut(addr, ":")
			if !ok {
				log.Fatalf("invalid addr: %s", addr)
			}

			httpPort := portToInt(port) + 1
			url := fmt.Sprintf("http://%s:%d/raft/status", host, httpPort)

			// do HTTP GET
			client := http.Client{Timeout: 2 * time.Second}
			resp, err := client.Get(url)
			if err != nil {
				log.Fatalf("failed to fetch status: %v", err)
			}
			defer resp.Body.Close()

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				log.Fatalf("failed to read response: %v", err)
			}

			fmt.Println(string(body))
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
