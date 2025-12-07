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

	"encoding/json"

	"github.com/jerkeyray/mimori/internal/api/kv"
	"github.com/jerkeyray/mimori/internal/raft"
	"github.com/jerkeyray/mimori/internal/raft/raftpb"
	"github.com/jerkeyray/mimori/internal/storage"
)

// this file defines how our server responds to client commands(mimorictl)

// gRPC service implementation
type Server struct {
	kv.UnimplementedKVServer
	store storage.KV // pebble wrapper
	raft  *raft.Raft
}

func NewServer(store storage.KV, r *raft.Raft) *Server {
	return &Server{store: store, raft: r}
}

// gRPC method implementations

func (s *Server) Put(ctx context.Context, req *kv.PutRequest) (*kv.PutResponse, error) {
	if !s.raft.IsLeader() {
		// TODO: encode leader redirect error later
		return nil, fmt.Errorf("not leader")
	}

	data := encodePutCmd(req.Key, req.Value)

	_, err := s.raft.Propose(data)
	if err != nil {
		return nil, err
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
	if !s.raft.IsLeader() {
		return nil, fmt.Errorf("not leader")
	}

	data := encodeDeleteCmd(req.Key)

	_, err := s.raft.Propose(data)
	if err != nil {
		return nil, err
	}

	return &kv.DeleteResponse{Deleted: true}, nil
}

func (s *Server) Health(ctx context.Context, _ *kv.HealthRequest) (*kv.HealthResponse, error) {
	return &kv.HealthResponse{Status: "ok"}, nil
}

// server launcher
func ListenAndServe(addr string, store storage.KV, raftNode *raft.Raft) error {
	// listen on the main gRPC address
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	// HTTP health endpoint
	// if node listens on :4000, HTTP health runs on :4001
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

	// create gRPC server
	grpcServer := grpc.NewServer()

	// register KV service
	kv.RegisterKVServer(grpcServer, NewServer(store, raftNode))

	// register raft RPC service
	raftpb.RegisterRaftServer(grpcServer, raftNode)

	fmt.Printf("Mimori node listening on %s\n", addr)
	return grpcServer.Serve(lis)
}

// extracts port from string and returns the number
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

type raftCommand struct {
	Op    raft.CommandType `json:"op"`
	Key   []byte           `json:"key"`
	Value []byte           `json:"value,omitempty"`
}

func encodePutCmd(key, val []byte) []byte {
	cmd := raftCommand{Op: raft.CmdPut, Key: key, Value: val}
	b, _ := json.Marshal(cmd)
	return b
}

func encodeDeleteCmd(key []byte) []byte {
	cmd := raftCommand{Op: raft.CmdDelete, Key: key}
	b, _ := json.Marshal(cmd)
	return b
}
