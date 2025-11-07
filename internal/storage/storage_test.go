package storage

import (
	"os"
	"testing"
)

func TestPebbleKV(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	key := []byte("name")
	val := []byte("key")

	if err := db.Put(key, val); err != nil {
		t.Fatalf("put failed: %v", err)
	}

	got, ok, err := db.Get(key)
	if err != nil || !ok {
		t.Fatalf("get failed: %v", err)
	}
	if string(got) != string(val) {
		t.Fatalf("expected %s, got %s", val, got)
	}

	if err := db.Delete(key); err != nil {
		t.Fatalf("delete failed: %v", err)
	}

	_, ok, _ = db.Get(key)
	if ok {
		t.Fatalf("expected key to be gone")
	}
	_ = os.RemoveAll(dir)
}

