package storage

import (
	"github.com/cockroachdb/pebble"
)

// interface implemented by PebbleKV used by gRPC
type KV interface {
	Put(key, value []byte) error
	Get(key []byte) ([]byte, bool, error)
	Delete(key []byte) error
	Close() error
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
func (p *PebbleKV) Put (key, value []byte) error {
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



