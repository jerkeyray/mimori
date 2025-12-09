package raft

import (
	"encoding/json"
	"os"
	"sync"
)

type metaData struct {
	Term     int    `json:"term"`
	VotedFor string `json:"voted_for"`
}

type MetaStore struct {
	path string
	mu   sync.Mutex
}

func NewMetaStore(path string) *MetaStore {
	return &MetaStore{path: path}
}

// Load term + votedFor from disk
func (m *MetaStore) Load() (int, NodeID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	f, err := os.ReadFile(m.path)
	if err != nil {
		// missing file is NOT an error, it's first boot
		return 0, "", nil
	}

	var md metaData
	if err := json.Unmarshal(f, &md); err != nil {
		return 0, "", err
	}

	return md.Term, NodeID(md.VotedFor), nil
}

// Save term + votedFor
func (m *MetaStore) Save(term int, votedFor NodeID) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	md := metaData{
		Term:     term,
		VotedFor: string(votedFor),
	}
	b, err := json.Marshal(md)
	if err != nil {
		return err
	}

	return os.WriteFile(m.path, b, 0644)
}
