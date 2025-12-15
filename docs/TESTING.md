# Testing

## Go tests

```bash
# All packages
go test ./...

# Integration tests only
go test ./tests
```

### Notes for restricted environments (CI / sandboxes)

- **Go build cache permissions**: if your environment can’t write to the default Go build cache directory, set `GOCACHE` to a writable path in the repo:

```bash
mkdir -p .gocache
GOCACHE="$PWD/.gocache" go test ./... -count=1
```

- **Network-binding tests**: some tests bind to `127.0.0.1` and will be skipped unless explicitly enabled:

```bash
MIMORI_ENABLE_NET_TESTS=1 go test ./internal/cluster -count=1
```

## Shell helpers

All shell scripts live under `scripts/`.

```bash
# Dashboard (local, one node)
bash scripts/dashboard-start.sh

# Reset local data/processes
bash scripts/reset-cluster.sh

# Dynamic membership smoke test
bash scripts/tests/cluster-membership.sh

# Follower reads smoke test
bash scripts/tests/follower-reads.sh

# Leader transfer smoke test
bash scripts/tests/leader-transfer.sh
```
