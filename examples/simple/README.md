# Simple Mimori Client Example

This example demonstrates basic usage of the Mimori Go client library.

## Prerequisites

A running Mimori cluster on the default ports:

```bash
cd ../..
docker-compose up -d
```

Or manually start nodes on ports 4000, 4002, 4004.

## Run

```bash
go run main.go
```

## What it does

1. Connects to the cluster using seed addresses
2. Performs health check
3. Writes several key-value pairs
4. Reads with strong consistency (from leader)
5. Reads with stale consistency (from followers)
6. Deletes a key and verifies deletion
7. Demonstrates context timeout usage

## Output

```text
Connected to Mimori cluster

[1] Health check...
    Cluster is healthy

[2] Writing data...
    Wrote: user:alice
    Wrote: user:bob
    Wrote: config:ttl

[3] Reading data (strong consistency)...
    user:alice = {"name":"Alice","role":"admin"}

[4] Reading data (allow stale)...
    config:ttl = 300 (may be slightly stale)

[5] Deleting data...
    Deleted: user:bob

[6] Verified: user:bob no longer exists

[7] Using context timeout...
    Put succeeded within timeout

Done!
```
