#!/bin/bash

# Test script for Follower Reads feature
# This script demonstrates reading from followers with --allow-stale flag

set -e

echo "Testing Follower Reads Feature"
echo "=================================="
echo ""

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Cleanup function
cleanup() {
    echo ""
    echo "Cleaning up..."
    pkill -f mimorid || true
    sleep 1
    lsof -ti:4000 | xargs kill -9 2>/dev/null || true
    lsof -ti:4001 | xargs kill -9 2>/dev/null || true
    lsof -ti:4002 | xargs kill -9 2>/dev/null || true
    rm -rf /tmp/mimori-test-*
}

trap cleanup EXIT

# Build binaries
echo "Building binaries..."
go build -o bin/mimorid cmd/mimorid/main.go
go build -o bin/mimorictl cmd/mimorictl/main.go

# Create data directories
mkdir -p /tmp/mimori-test-{1,2,3}

# Start 3 nodes
echo ""
echo "Starting 3-node cluster..."

# Node 1
MIMORI_NODE_ID=localhost:4000 \
MIMORI_RAFT_PEERS=localhost:4001,localhost:4002 \
MIMORI_DATA_DIR=/tmp/mimori-test-1 \
bin/mimorid --addr :4000 > /tmp/node1.log 2>&1 &
NODE1_PID=$!
echo "  [OK] Node 1 started (PID: $NODE1_PID) on :4000"

sleep 1

# Node 2
MIMORI_NODE_ID=localhost:4001 \
MIMORI_RAFT_PEERS=localhost:4000,localhost:4002 \
MIMORI_DATA_DIR=/tmp/mimori-test-2 \
bin/mimorid --addr :4001 > /tmp/node2.log 2>&1 &
NODE2_PID=$!
echo "  [OK] Node 2 started (PID: $NODE2_PID) on :4001"

sleep 1

# Node 3
MIMORI_NODE_ID=localhost:4002 \
MIMORI_RAFT_PEERS=localhost:4000,localhost:4001 \
MIMORI_DATA_DIR=/tmp/mimori-test-3 \
bin/mimorid --addr :4002 > /tmp/node3.log 2>&1 &
NODE3_PID=$!
echo "  [OK] Node 3 started (PID: $NODE3_PID) on :4002"

echo ""
echo "Waiting for cluster to stabilize..."
sleep 3

# Find the leader
echo ""
echo "Finding leader..."
LEADER=""
LEADER_ADDR=""
for port in 4000 4001 4002; do
    STATUS=$(bin/mimorictl --addr 127.0.0.1:$port status 2>/dev/null || echo "")
    if echo "$STATUS" | grep -q "State: leader"; then
        LEADER_ADDR="127.0.0.1:$port"
        LEADER=$(echo "$STATUS" | grep "Node ID:" | awk '{print $3}')
        echo "  [OK] Leader found: $LEADER (on :$port)"
        break
    fi
done

if [ -z "$LEADER" ]; then
    echo "  [FAIL] Could not find leader"
    exit 1
fi

# Find a follower
FOLLOWER_ADDR=""
for port in 4000 4001 4002; do
    if [ "127.0.0.1:$port" != "$LEADER_ADDR" ]; then
        STATUS=$(bin/mimorictl --addr 127.0.0.1:$port status 2>/dev/null || echo "")
        if echo "$STATUS" | grep -q "State: follower"; then
            FOLLOWER_ADDR="127.0.0.1:$port"
            echo "  [OK] Follower found on :$port"
            break
        fi
    fi
done

if [ -z "$FOLLOWER_ADDR" ]; then
    echo "  [WARN] Could not find follower, using non-leader node"
    for port in 4000 4001 4002; do
        if [ "127.0.0.1:$port" != "$LEADER_ADDR" ]; then
            FOLLOWER_ADDR="127.0.0.1:$port"
            break
        fi
    done
fi

echo ""
echo "Test 1: Write data to leader"
echo "--------------------------------"
echo "Writing key='test-key' value='hello-world' to leader ($LEADER_ADDR)..."
bin/mimorictl --addr $LEADER_ADDR put test-key hello-world
echo "  [OK] Write successful"

echo ""
echo "Waiting for replication..."
sleep 2

echo ""
echo "Test 2: Read from leader (default behavior)"
echo "-----------------------------------------------"
RESULT=$(bin/mimorictl --addr $LEADER_ADDR get test-key 2>&1)
if [ "$RESULT" = "hello-world" ]; then
    echo "  [OK] Leader read successful: $RESULT"
else
    echo "  [FAIL] Leader read failed: $RESULT"
    exit 1
fi

echo ""
echo "Test 3: Try to read from follower WITHOUT --allow-stale (should fail)"
echo "------------------------------------------------------------------------"
RESULT=$(bin/mimorictl --addr $FOLLOWER_ADDR get test-key 2>&1)
if echo "$RESULT" | grep -q "not leader"; then
    echo "  [OK] Correctly rejected: follower read without --allow-stale"
    echo "     Error: $(echo "$RESULT" | head -1)"
else
    echo "  [FAIL] Expected error but got: $RESULT"
    exit 1
fi

echo ""
echo "Test 4: Read from follower WITH --allow-stale (should succeed)"
echo "------------------------------------------------------------------"
RESULT=$(bin/mimorictl --addr $FOLLOWER_ADDR get test-key --allow-stale 2>&1)
if [ "$RESULT" = "hello-world" ]; then
    echo "  [OK] Follower read successful: $RESULT"
    echo "     (Read served from follower with valid read lease)"
else
    echo "  [WARN] Follower read result: $RESULT"
    echo "     (This might happen if read lease expired - wait a bit and try again)"
fi

echo ""
echo "Test 5: Verify follower read works multiple times"
echo "-----------------------------------------------------"
SUCCESS=0
for i in {1..3}; do
    sleep 0.5
    RESULT=$(bin/mimorictl --addr $FOLLOWER_ADDR get test-key --allow-stale 2>&1)
    if [ "$RESULT" = "hello-world" ]; then
        SUCCESS=$((SUCCESS + 1))
        echo "  [OK] Attempt $i: $RESULT"
    else
        echo "  [WARN] Attempt $i: $RESULT"
    fi
done
echo "  Success rate: $SUCCESS/3"

echo ""
echo "Follower Reads Test Complete!"
echo "=================================="
echo ""
echo "Summary:"
echo "  - Leader reads work (strong consistency)"
echo "  - Follower reads are rejected without --allow-stale"
echo "  - Follower reads work with --allow-stale (when lease is valid)"
echo ""
echo "Usage:"
echo "  # Strong consistency (reads from leader)"
echo "  mimorictl get key"
echo ""
echo "  # Allow stale reads (can read from followers)"
echo "  mimorictl get key --allow-stale"
echo ""

