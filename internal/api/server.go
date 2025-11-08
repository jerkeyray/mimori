package api

import (
	"context"
	"fmt"
	"net"

	"google.golang.org/grpc"

	"github.com/jerkeyray/mimori/internal/api/kv"
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
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	grpcServer := grpc.NewServer()
	// route calls to KV service to this implementation
	kv.RegisterKVServer(grpcServer, NewServer(store))
	fmt.Printf("Mimori node listening on %s\n", addr)
	return grpcServer.Serve(lis)
}
