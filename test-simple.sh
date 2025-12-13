#!/bin/bash
# Simple manual test for dynamic cluster membership

echo "=== Simple Cluster Membership Test ==="
echo ""
echo "This is a simple test. For comprehensive testing, use test-cluster-membership.sh"
echo ""

# Build if needed
if [ ! -f "./mimorid" ]; then
    echo "Building binaries..."
    go build -o mimorid ./cmd/mimorid
    go build -o mimorictl ./cmd/mimorictl
fi

# Cleanup
rm -rf ./test-data-*

echo "Step 1: Starting Node 1 (this will be the initial leader)"
echo "Run this in a separate terminal:"
echo ""
echo "  MIMORI_ADDR=:4000 MIMORI_NODE_ID=:4000 MIMORI_DATA=./test-data-node1 ./mimorid"
echo ""
echo "Press Enter when Node 1 is running and shows 'Mimori node listening on :4000'..."
read

echo ""
echo "Step 2: Verifying Node 1 is leader..."
./mimorictl --addr localhost:4000 status
echo ""

echo "Step 3: Starting Node 2"
echo "Run this in another terminal:"
echo ""
echo "  MIMORI_ADDR=:4002 MIMORI_NODE_ID=:4002 MIMORI_DATA=./test-data-node2 MIMORI_PEERS=:4000 ./mimorid"
echo ""
echo "Press Enter when Node 2 is running..."
read

echo ""
echo "Step 4: Adding Node 2 to cluster dynamically..."
./mimorictl --addr localhost:4000 add-node :4002
echo ""

echo "Step 5: Verifying both nodes know about each other..."
echo "Node 1 status:"
./mimorictl --addr localhost:4000 status | head -10
echo ""
echo "Node 2 status:"
./mimorictl --addr localhost:4002 status | head -10
echo ""

echo "Step 6: Testing data replication..."
./mimorictl --addr localhost:4000 put testkey "test-value"
sleep 1
echo "Reading from Node 1:"
./mimorictl --addr localhost:4000 get testkey
echo "Reading from Node 2:"
./mimorictl --addr localhost:4002 get testkey
echo ""

echo "Step 7: Removing Node 2..."
./mimorictl --addr localhost:4000 remove-node :4002
echo ""

echo "Step 8: Verifying Node 2 is removed..."
./mimorictl --addr localhost:4000 status | head -10
echo ""

echo "=== Test Complete ==="
echo "Stop the nodes with Ctrl+C in their respective terminals"
echo "Clean up with: rm -rf ./test-data-*"

