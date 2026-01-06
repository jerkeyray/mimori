package raft

import (
	"encoding/json"
	"os"
	"sync"
)

// on-disk representation of a log entry
type logDiskEntry struct {
	Index int    `json:"index"`
	Term  int    `json:"term"`
	Data  []byte `json:"data"`
}

// LogStore manages saving/loading the raft log from disk
type LogStore struct {
	path string
	mu   sync.Mutex
}

// create a new log store at the given path
func NewLogStore(path string) *LogStore {
	return &LogStore{path: path}
}

// Load reads the full log from disk.
// if file is missing, returns empty slice (caller will seed dummy entry)
func (ls *LogStore) Load() ([]LogEntry, error) {
	ls.mu.Lock()
	defer ls.mu.Unlock()

	b, err := os.ReadFile(ls.path)
	if err != nil {
		// first boot, no log yet
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var diskEntries []logDiskEntry
	if err := json.Unmarshal(b, &diskEntries); err != nil {
		return nil, err
	}

	out := make([]LogEntry, 0, len(diskEntries))
	for _, de := range diskEntries {
		out = append(out, LogEntry{
			Index: de.Index,
			Term:  de.Term,
			Data:  append([]byte(nil), de.Data...), // copy
		})
	}
	return out, nil
}

// SaveAll overwrites the on-disk log with the full slice of entries
func (ls *LogStore) SaveAll(entries []LogEntry) error {
	ls.mu.Lock()
	defer ls.mu.Unlock()

	diskEntries := make([]logDiskEntry, 0, len(entries))
	for _, e := range entries {
		diskEntries = append(diskEntries, logDiskEntry{
			Index: e.Index,
			Term:  e.Term,
			Data:  append([]byte(nil), e.Data...), // copy
		})
	}

	b, err := json.Marshal(diskEntries)
	if err != nil {
		return err
	}

	// Write atomically: write to temp file, then rename
	// This prevents corruption if process crashes mid-write
	tmp := ls.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, ls.path)
}
