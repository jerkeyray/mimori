package raft

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Snapshot metadata and payload
type Snapshot struct {
	LastIncludedIndex int    `json:"last_included_index"`
	LastIncludedTerm  int    `json:"last_included_term"`
	CreatedAt         int64  `json:"created_at"`
	Data              []byte `json:"data"` // opaque state machine bytes (JSON map in our case)
}

type SnapshotStore struct {
	path string
	mu   sync.Mutex
}

func NewSnapshotStore(path string) *SnapshotStore {
	return &SnapshotStore{path: path}
}

func (s *SnapshotStore) Save(snap *Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	b, err := json.Marshal(snap)
	if err != nil {
		return err
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return err
	}

	return os.Rename(tmp, s.path)
}

func (s *SnapshotStore) Load() (*Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var snap Snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return nil, err
	}

	return &snap, nil
}

func (s *SnapshotStore) Path() string { return s.path }

// helper to bulid path join quickly
func SnapshotPath(dataDir string) string {
	return filepath.Join(dataDir, "raft-snap.json")
}
