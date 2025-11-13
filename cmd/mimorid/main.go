package main

import (
	"log"
	"os"
	"strings"

	"github.com/jerkeyray/mimori/internal/api"
	"github.com/jerkeyray/mimori/internal/cluster"
	"github.com/jerkeyray/mimori/internal/raft"
	"github.com/jerkeyray/mimori/internal/storage"
)

func main() {
	addr := env("MIMORI_ADDR", ":4000")
	dataDir := env("MIMORI_DATA", "data")

	peerList := splitPeers(env("MIMORI_PEERS", ""))

	store, err := storage.Open(dataDir)
	if err != nil {
		log.Fatalf("failed to open storage: %v", err)
	}
	defer store.Close()

	raftNode := raft.New(raft.NodeID(addr), convertPeersToNodeIDs(peerList))
	_ = raftNode // save it later when we integrate RPC

	clusterMgr := cluster.New(addr, peerList)
	clusterMgr.Start()
	defer clusterMgr.Stop()


	if err := api.ListenAndServe(addr, store); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// take []string, filter out empty ones, return slice of NodeIDs
func convertPeersToNodeIDs(peers []string) []raft.NodeID {
	out := make([]raft.NodeID, 0, len(peers))
	for _, p := range peers {
		if p != "" {
			out = append(out, raft.NodeID(p))
		}
	}
	return out
}

// splitPeers turns "a,b,c" into []string and filters out empties.
func splitPeers(s string) []string {
	if s == "" {
		return []string{}
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
