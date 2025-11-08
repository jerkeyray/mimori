package main

import (
	"log"
	"os"

	"github.com/jerkeyray/mimori/internal/api"
	"github.com/jerkeyray/mimori/internal/storage"
)

func main() {
	addr := env("MIMORI_ADDR", ":4000")
	dataDir := env("MIMORI_DATA", "data")

	store, err := storage.Open(dataDir)
	if err != nil {
		log.Fatalf("failed to open storage: %v", err)
	}
	defer store.Close()

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
