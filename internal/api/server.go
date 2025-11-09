package api

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"

	"google.golang.org/grpc"

	"github.com/jerkeyray/mimori/internal/api/kv"
	"github.com/jerkeyray/mimori/internal/cluster"
	"github.com/jerkeyray/mimori/internal/storage"
)

// gRPC service implementation
type Server struct {
	kv.UnimplementedKVServer
	store storage.KV // pebble wrapper
}

func NewServer(store storage.KV) *Server {
	return &Server{store: store}
}

// gRPC method implementations

func (s *Server) Put(ctx context.Context, req *kv.PutRequest) (*kv.PutResponse, error) {
	err := s.store.Put(req.Key, req.Value)
	if err != nil {
		return &kv.PutResponse{Ok: false}, err
	}
	return &kv.PutResponse{Ok: true}, nil
}

func (s *Server) Get(ctx context.Context, req *kv.GetRequest) (*kv.GetResponse, error) {
	val, found, err := s.store.Get(req.Key)
	if err != nil {
		return nil, err
	}
	return &kv.GetResponse{Value: val, Found: found}, nil
}

func (s *Server) Delete(ctx context.Context, req *kv.DeleteRequest) (*kv.DeleteResponse, error) {
	err := s.store.Delete(req.Key)
	if err != nil {
		return nil, err
	}
	return &kv.DeleteResponse{Deleted: true}, nil
}

func (s *Server) Health(ctx context.Context, _ *kv.HealthRequest) (*kv.HealthResponse, error) {
	return &kv.HealthResponse{Status: "ok"}, nil
}

// server launcher
func ListenAndServe(addr string, store storage.KV) error {
	// start the cluster heartbeat routine
	peers := []string{":4000", ":4001", ":4002"} // temporary static config
	cluster := cluster.New(addr, peers)
	cluster.Start()
	defer cluster.Stop()

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	// HTTP health endpoint
	go func() {
		httpPort := parsePort(addr) + 1
		httpAddr := fmt.Sprintf(":%d", httpPort)
		http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		})
		log.Printf("[http] health endpoint at %s", httpAddr)
		_ = http.ListenAndServe(httpAddr, nil)
	}()

	grpcServer := grpc.NewServer()
	// route calls to KV service to this implementation
	kv.RegisterKVServer(grpcServer, NewServer(store))
	fmt.Printf("Mimori node listening on %s\n", addr)
	return grpcServer.Serve(lis)
}

// parsePort takes an address like ":4000" or "127.0.0.1:4000" and returns the numeric port.
// if parsing fails, it just returns 0 so the caller can handle it gracefully.
func parsePort(addr string) int {
	// split on colon, take the last part (the port)
	parts := strings.Split(addr, ":")
	if len(parts) == 0 {
		return 0
	}
	portStr := parts[len(parts)-1]
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0
	}
	return port
}

