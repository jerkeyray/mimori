package storage

import (
	"fmt"
	"sync"
	"testing"
)

func TestPebbleKV(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	// Test basic Put/Get
	key := []byte("name")
	val := []byte("mimori")

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

	// Test Delete
	if err := db.Delete(key); err != nil {
		t.Fatalf("delete failed: %v", err)
	}

	_, ok, _ = db.Get(key)
	if ok {
		t.Fatalf("expected key to be gone")
	}
}

func TestPebbleKV_Persistence(t *testing.T) {
	dir := t.TempDir()
	
	// Open and write data
	{
		db, err := Open(dir)
		if err != nil {
			t.Fatalf("failed to open db: %v", err)
		}
		if err := db.Put([]byte("persist"), []byte("true")); err != nil {
			t.Fatalf("put failed: %v", err)
		}
		db.Close()
	}

	// Reopen and verify data
	{
		db, err := Open(dir)
		if err != nil {
			t.Fatalf("failed to reopen db: %v", err)
		}
		defer db.Close()

		val, ok, err := db.Get([]byte("persist"))
		if err != nil {
			t.Fatalf("get failed: %v", err)
		}
		if !ok {
			t.Fatalf("key not found after reopen")
		}
		if string(val) != "true" {
			t.Fatalf("got %s, expected true", val)
		}
	}
}

func TestPebbleKV_Concurrency(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	var wg sync.WaitGroup
	n := 100

	// Concurrent writes
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := []byte(fmt.Sprintf("k-%d", i))
			val := []byte(fmt.Sprintf("v-%d", i))
			if err := db.Put(key, val); err != nil {
				t.Errorf("concurrent put failed: %v", err)
			}
		}(i)
	}
	wg.Wait()

	// Concurrent reads
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := []byte(fmt.Sprintf("k-%d", i))
			val, ok, err := db.Get(key)
			if err != nil {
				t.Errorf("concurrent get failed: %v", err)
			}
			if !ok {
				t.Errorf("key %s not found", key)
			}
			expected := fmt.Sprintf("v-%d", i)
			if string(val) != expected {
				t.Errorf("got %s, want %s", val, expected)
			}
		}(i)
	}
	wg.Wait()
}
