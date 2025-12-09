package main

import (
	"encoding/json"
	"log"
	"os"
	"strings"

	"github.com/jerkeyray/mimori/internal/api"
	"github.com/jerkeyray/mimori/internal/cluster"
	"github.com/jerkeyray/mimori/internal/raft"
	"github.com/jerkeyray/mimori/internal/storage"
)

// this is the bootloader for each node in mimori
// the primariy job is initialize all the isolated components
// and wire them all together (dependency injection)

func main() {
	addr := env("MIMORI_ADDR", ":4000")
	dataDir := env("MIMORI_DATA", "data")
	peerList := splitPeers(env("MIMORI_PEERS", ""))

	// open pebble DB
	store, err := storage.Open(dataDir)
	if err != nil {
		log.Fatalf("failed to open storage: %v", err)
	}
	defer store.Close()

	// give the network addr as the unique raft node id and give its peerList
	raftNode := raft.New(
		raft.NodeID(addr), convertPeersToNodeIDs(peerList), dataDir,
	)

	// state machine apply loop: decode cmds and apply to KV
	go func() {
		for entry := range raftNode.ApplyCh() {
			var cmd raft.Command
			if err := json.Unmarshal(entry.Data, &cmd); err != nil {
				log.Printf("failed to decode raft command: %v", err)
				continue
			}

			switch cmd.Op {
			case raft.CmdPut:
				if err := store.Put(cmd.Key, cmd.Value); err != nil {
					log.Printf("apply PUT failed: %v", err)
				}
			case raft.CmdDelete:
				if err := store.Delete(cmd.Key); err != nil {
					log.Printf("apply DELETE failed: %v", err)
				}
			default:
				log.Printf("unknown command op: %v", cmd.Op)
			}
		}
	}()

	// start cluster heartbeat manager
	clusterMgr := cluster.New(addr, peerList)
	clusterMgr.Start()
	defer clusterMgr.Stop()

	// start the gRPC server and wait for connection calls
	// pass the initialized store and raft node into the API layer
	if err := api.ListenAndServe(addr, store, raftNode); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

// read env or fall back to default
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
