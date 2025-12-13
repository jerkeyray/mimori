# Testing Dynamic Cluster Membership

This guide shows you how to test the dynamic cluster membership feature (adding and removing nodes at runtime).

## Quick Test Script

The easiest way to test is using the automated test script:

```bash
./test-cluster-membership.sh
```

This script will:
1. Start a 3-node cluster
2. Add nodes dynamically
3. Test data replication
4. Remove nodes
5. Verify the cluster continues working

## Manual Testing

### Step 1: Build the Binaries

```bash
go build -o mimorid ./cmd/mimorid
go build -o mimorictl ./cmd/mimorictl
```

### Step 2: Start the First Node (Leader)

In terminal 1:
```bash
# Clean old data
rm -rf ./data1

# Start node 1
MIMORI_ADDR=:4000 MIMORI_NODE_ID=:4000 MIMORI_DATA=./data1 ./mimorid
```

Wait a few seconds for it to become leader, then verify:
```bash
./mimorictl --addr localhost:4000 status
```

You should see `State: leader`.

### Step 3: Start Second Node

In terminal 2:
```bash
rm -rf ./data2
MIMORI_ADDR=:4002 MIMORI_NODE_ID=:4002 MIMORI_DATA=./data2 MIMORI_PEERS=:4000 ./mimorid
```

### Step 4: Add Node 2 to Cluster Dynamically

In terminal 3 (or your main terminal):
```bash
# Add the node to the cluster
./mimorictl --addr localhost:4000 add-node :4002

# Verify it was added - check status on both nodes
./mimorictl --addr localhost:4000 status
./mimorictl --addr localhost:4002 status
```

You should see that both nodes know about each other in the peer list.

### Step 5: Test Data Replication

```bash
# Write data through the leader
./mimorictl --addr localhost:4000 put mykey "hello from leader"

# Read from both nodes (both should have the data)
./mimorictl --addr localhost:4000 get mykey
./mimorictl --addr localhost:4002 get mykey
```

### Step 6: Add a Third Node

In terminal 4:
```bash
rm -rf ./data3
MIMORI_ADDR=:4004 MIMORI_NODE_ID=:4004 MIMORI_DATA=./data3 MIMORI_PEERS=:4000 ./mimorid
```

Then add it:
```bash
./mimorictl --addr localhost:4000 add-node :4004
```

Verify all 3 nodes:
```bash
for port in 4000 4002 4004; do
    echo "=== Node $port ==="
    ./mimorictl --addr localhost:$port status
done
```

### Step 7: Remove a Node

```bash
# Remove node 3
./mimorictl --addr localhost:4000 remove-node :4004

# Verify it was removed
./mimorictl --addr localhost:4000 status
./mimorictl --addr localhost:4002 status
```

Node 3 should no longer be in the peer list. You can stop the node 3 process.

### Step 8: Test Error Cases

```bash
# Try to remove the last peer (should fail)
./mimorictl --addr localhost:4000 remove-node :4002
# Expected: "cannot remove last peer"

# Try to add a node that doesn't exist (won't fail, but node won't be reachable)
./mimorictl --addr localhost:4000 add-node :9999
```

## Using Docker Compose

You can also test with Docker, but note that nodes need to be added with their container names:

```bash
# Start the cluster
docker-compose up -d

# Wait for nodes to be ready
sleep 5

# Add a new node (if you add one to docker-compose.yml)
./mimorictl --addr localhost:4000 add-node mimori-node4:4000

# Remove a node
./mimorictl --addr localhost:4000 remove-node mimori-node3:4000
```

## Testing with Unit Tests

There's also a unit test you can run:

```bash
go test ./internal/raft -v -run TestRaft
```

## What to Verify

1. **Adding nodes works**: New nodes appear in all nodes' peer lists
2. **Removing nodes works**: Removed nodes disappear from peer lists
3. **Data replication continues**: Data written after membership changes is replicated correctly
4. **Leader election still works**: If you remove the leader, a new leader is elected
5. **Safety checks work**: Can't remove the last peer

## Troubleshooting

- **"not leader" error**: Make sure you're running the command against the leader. Use `./mimorictl leader` to find the leader.
- **Node not connecting**: Make sure the node is actually running and the address/port is correct.
- **Changes not reflecting**: Wait a second or two for the configuration change to replicate.
- **Port conflicts**: Make sure ports 4000, 4002, 4004 are not in use by other processes.

