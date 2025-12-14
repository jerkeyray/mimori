# Testing

## Go tests

```bash
go test ./...
go test ./tests
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


