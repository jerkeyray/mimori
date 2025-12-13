# Quick Test Guide for Dynamic Cluster Membership

## Fastest Way to Test

### Option 1: Automated Script (Recommended)

```bash
# Run the comprehensive test script
./test-cluster-membership.sh
```

This runs all tests automatically and shows you the results.

### Option 2: Manual Step-by-Step (5 minutes)

**Terminal 1 - Start Node 1:**
```bash
rm -rf ./data1
MIMORI_ADDR=:4000 MIMORI_NODE_ID=localhost:4000 MIMORI_DATA=./data1 ./mimorid
```

Wait for it to show "Mimori node listening on :4000", then in a new terminal:

**Terminal 2 - Start Node 2:**
```bash
rm -rf ./data2  
MIMORI_ADDR=:4002 MIMORI_NODE_ID=localhost:4002 MIMORI_DATA=./data2 MIMORI_PEERS=localhost:4000 ./mimorid
```

**Terminal 3 - Test adding node:**
```bash
# Check Node 1 status
./mimorictl --addr localhost:4000 status

# Add Node 2 dynamically
./mimorictl --addr localhost:4000 add-node localhost:4002

# Verify it worked - check both nodes
./mimorictl --addr localhost:4000 status
./mimorictl --addr localhost:4002 status
```

**Test data replication:**
```bash
# Write data
./mimorictl --addr localhost:4000 put mykey "hello world"

# Read from both nodes
./mimorictl --addr localhost:4000 get mykey
./mimorictl --addr localhost:4002 get mykey
```

**Test removing node:**
```bash
# Remove Node 2
./mimorictl --addr localhost:4000 remove-node localhost:4002

# Verify
./mimorictl --addr localhost:4000 status
```

## Important Notes

1. **Node IDs vs Addresses**: 
   - `MIMORI_ADDR` is where the node listens (e.g., `:4000`)
   - `MIMORI_NODE_ID` is the unique identifier for Raft (e.g., `localhost:4000`)
   - When adding nodes, use the same format as `MIMORI_NODE_ID`

2. **Leader Required**: Add/remove commands must be run against the leader. Use `./mimorictl leader` to find it.

3. **Wait Times**: After adding/removing nodes, wait 1-2 seconds for replication.

4. **Cleanup**: Stop all nodes (Ctrl+C) and remove data dirs:
   ```bash
   rm -rf ./data{1,2,3}
   ```

## What to Look For

✅ **Success indicators:**
- `add-node` returns "Node X added to cluster successfully"
- Status shows the new node in peer counts
- Data replicates to new nodes
- `remove-node` removes the node from peer lists

❌ **Error indicators:**
- "not leader" - run command against leader
- "cannot remove last peer" - expected error, cluster needs at least 2 nodes
- Connection errors - check node is running and address is correct

## Troubleshooting

- **"not leader"**: Find leader with `./mimorictl leader` and use that node
- **Node not connecting**: Ensure node IDs match exactly (e.g., `localhost:4000` not `:4000`)
- **Changes not showing**: Wait a few seconds, then check status again

