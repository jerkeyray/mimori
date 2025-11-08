package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/jerkeyray/mimori/internal/api/kv"
)

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
			client := mustConnect()
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()

			_, err := client.Client.Put(ctx, &kv.PutRequest{Key: key, Value: val})
			if err != nil {
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
		Use:   "del [key]",
		Short: "Delete a key from the database",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			key := []byte(args[0])
			client := mustConnect()
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()

			_, err := client.Client.Delete(ctx, &kv.DeleteRequest{Key: key})
			if err != nil {
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
	Client kv.KVClient
	conn   *grpc.ClientConn
}

// Close releases the underlying gRPC connection resources.
func (cw *clientWrapper) Close() {
	if cw.conn != nil {
		_ = cw.conn.Close()
	}
}

func mustConnect() *clientWrapper {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// grpc.DialContext is the stable, modern connection call.
	conn, err := grpc.DialContext(
		ctx,
		addr, // from the global flag
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalf("failed to connect to node at %s: %v", addr, err)
	}

	client := kv.NewKVClient(conn)
	return &clientWrapper{Client: client, conn: conn}
}


