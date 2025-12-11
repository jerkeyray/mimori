package storage

import (
	"encoding/json"

	"github.com/cockroachdb/pebble"
)

// interface implemented by PebbleKV used by gRPC
type KV interface {
	Put(key, value []byte) error
	Get(key []byte) ([]byte, bool, error)
	Delete(key []byte) error
	Close() error

	Snapshot() ([]byte, error)
	Restore(data []byte) error
}

// PebbleKV is a wrapper aroung the actual Pebble db
type PebbleKV struct {
	db *pebble.DB
}

// open or create the pebble db at the given path
func Open(path string) (*PebbleKV, error) {
	db, err := pebble.Open(path, &pebble.Options{})
	if err != nil {
		return nil, err
	}

	return &PebbleKV{db: db}, nil
}

// put writes the kv pair to disk
// pebble.Sync ensures the data actually gets saved to the disk, not memory buffers
func (p *PebbleKV) Put(key, value []byte) error {
	return p.db.Set(key, value, pebble.Sync)
}

// fetch the kv pair from the pebble db
func (p *PebbleKV) Get(key []byte) ([]byte, bool, error) {
	v, closer, err := p.db.Get(key)

	if err == pebble.ErrNotFound {
		return nil, false, nil
	}

	if err != nil {
		return nil, false, err
	}

	defer closer.Close()

	cp := append([]byte(nil), v...)
	return cp, true, nil
}

// delete the kv pair from disk, pebble.Sync to persist the deletion
func (p *PebbleKV) Delete(key []byte) error {
	return p.db.Delete(key, pebble.Sync)
}

// gracefully shutdown the db
func (p *PebbleKV) Close() error {
	return p.db.Close()
}

// Snapshot dumps the entire DB into JSON { "key": "value" }
func (p *PebbleKV) Snapshot() ([]byte, error) {
	it, err := p.db.NewIter(nil)
	if err != nil {
		return nil, err
	}
	defer it.Close()

	snapshotMap := make(map[string][]byte)

	for it.First(); it.Valid(); it.Next() {
		key := append([]byte(nil), it.Key()...)
		val := append([]byte(nil), it.Value()...)
		snapshotMap[string(key)] = val
	}

	return json.Marshal(snapshotMap)
}

// Restore wipes all data and loads the snapshot
func (p *PebbleKV) Restore(data []byte) error {
	// empty snapshot — treat as empty DB
	if data == nil || len(data) == 0 {
		return p.reset()
	}

	var snapshotMap map[string][]byte
	if err := json.Unmarshal(data, &snapshotMap); err != nil {
		return err
	}

	// wipe DB before restoring
	if err := p.reset(); err != nil {
		return err
	}

	// restore all keys
	batch := p.db.NewBatch()
	for k, v := range snapshotMap {
		if err := batch.Set([]byte(k), v, nil); err != nil {
			return err
		}
	}

	return batch.Commit(pebble.Sync)
}

func (p *PebbleKV) reset() error {
	// Pebble does not have a native "clear all" so we do a range deletion
	return p.db.DeleteRange(nil, nil, pebble.Sync)
}
