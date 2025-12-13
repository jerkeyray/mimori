package raft

import (
	"encoding/json"
	"os"
	"sync"
)

type metaData struct {
	Term     int      `json:"term"`
	VotedFor string   `json:"voted_for"`
	Peers    []string `json:"peers,omitempty"` // Cluster configuration
}

type MetaStore struct {
	path string
	mu   sync.Mutex
}

func NewMetaStore(path string) *MetaStore {
	return &MetaStore{path: path}
}

// Load term + votedFor + peers from disk
func (m *MetaStore) Load() (int, NodeID, []NodeID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	f, err := os.ReadFile(m.path)
	if err != nil {
		// missing file is NOT an error, it's first boot
		return 0, "", nil, nil
	}

	var md metaData
	if err := json.Unmarshal(f, &md); err != nil {
		return 0, "", nil, err
	}

	peers := make([]NodeID, len(md.Peers))
	for i, p := range md.Peers {
		peers[i] = NodeID(p)
	}

	return md.Term, NodeID(md.VotedFor), peers, nil
}

// Save term + votedFor + peers
func (m *MetaStore) Save(term int, votedFor NodeID, peers []NodeID) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	peerStrs := make([]string, len(peers))
	for i, p := range peers {
		peerStrs[i] = string(p)
	}

	md := metaData{
		Term:     term,
		VotedFor: string(votedFor),
		Peers:    peerStrs,
	}
	b, err := json.Marshal(md)
	if err != nil {
		return err
	}

	return os.WriteFile(m.path, b, 0644)
}
