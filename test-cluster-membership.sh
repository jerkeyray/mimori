#!/bin/bash
# Test script for dynamic cluster membership

set -e

# Colors for output
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

echo -e "${BLUE}=== Mimori Dynamic Cluster Membership Test ===${NC}\n"

# Always rebuild to ensure we have the latest code with AddNode/RemoveNode
echo -e "${YELLOW}Building binaries (ensuring latest code with AddNode/RemoveNode)...${NC}"
go build -o mimorid ./cmd/mimorid
if [ $? -ne 0 ]; then
    echo -e "${RED}Build of mimorid failed!${NC}"
    exit 1
fi
go build -o mimorictl ./cmd/mimorictl
if [ $? -ne 0 ]; then
    echo -e "${RED}Build of mimorictl failed!${NC}"
    exit 1
fi
echo -e "${GREEN}✓ Binaries built successfully${NC}\n"

# Kill any existing mimorid processes
echo -e "${YELLOW}Cleaning up old processes...${NC}"
pkill -f "mimorid" 2>/dev/null || true
sleep 1

# Check if ports are still in use
for port in 4000 4002 4004; do
    if lsof -ti:$port > /dev/null 2>&1; then
        echo -e "${RED}Port $port is still in use! Trying to kill processes on that port...${NC}"
        lsof -ti:$port | xargs kill -9 2>/dev/null || true
        sleep 1
    fi
done

# Clean up old data directories
echo -e "${YELLOW}Cleaning up old data...${NC}"
rm -rf ./test-data-node{1,2,3,4}

# Start Node 1 (will become leader initially)
echo -e "\n${GREEN}Starting Node 1 on :4000...${NC}"
MIMORI_ADDR=:4000 MIMORI_NODE_ID=localhost:4000 MIMORI_DATA=./test-data-node1 ./mimorid &
NODE1_PID=$!
sleep 3

# Verify Node 1 started
if ! kill -0 $NODE1_PID 2>/dev/null; then
    echo -e "${RED}Node 1 failed to start! Check if port 4000 is available.${NC}"
    exit 1
fi

# Wait for node1 to become leader
echo -e "${YELLOW}Waiting for Node 1 to become leader...${NC}"
for i in {1..15}; do
    if ./mimorictl --addr localhost:4000 status 2>/dev/null | grep -q "leader"; then
        echo -e "${GREEN}Node 1 is leader!${NC}"
        break
    fi
    if [ $i -eq 15 ]; then
        echo -e "${RED}Node 1 failed to become leader. Check logs above.${NC}"
        kill $NODE1_PID 2>/dev/null
        exit 1
    fi
    sleep 1
done

# Start Node 2
echo -e "\n${GREEN}Starting Node 2 on :4002...${NC}"
MIMORI_ADDR=:4002 MIMORI_NODE_ID=localhost:4002 MIMORI_DATA=./test-data-node2 MIMORI_PEERS=localhost:4000 ./mimorid &
NODE2_PID=$!
sleep 3

# Verify Node 2 started
if ! kill -0 $NODE2_PID 2>/dev/null; then
    echo -e "${RED}Node 2 failed to start!${NC}"
    kill $NODE1_PID 2>/dev/null
    exit 1
fi

# Add Node 2 to cluster dynamically
echo -e "\n${BLUE}=== Test 1: Adding Node 2 to cluster ===${NC}"

# Find the current leader (might be Node 1 or Node 2 due to elections)
echo -e "${YELLOW}Finding current leader...${NC}"
LEADER_ADDR=""
for port in 4000 4002; do
    if ./mimorictl --addr localhost:$port status 2>/dev/null | grep -q "State:.*leader"; then
        LEADER_ADDR="localhost:$port"
        echo -e "${GREEN}Leader is at $LEADER_ADDR${NC}"
        break
    fi
done

if [ -z "$LEADER_ADDR" ]; then
    echo -e "${RED}No leader found! Cannot add node.${NC}"
    kill $NODE1_PID $NODE2_PID 2>/dev/null
    exit 1
fi

echo -e "${YELLOW}Adding localhost:4002 to cluster via leader at $LEADER_ADDR...${NC}"
if ! ./mimorictl --addr $LEADER_ADDR add-node localhost:4002; then
    echo -e "${RED}Failed to add node!${NC}"
    kill $NODE1_PID $NODE2_PID 2>/dev/null
    exit 1
fi
sleep 2

# Verify Node 2 is in cluster
echo -e "\n${YELLOW}Checking cluster status...${NC}"
./mimorictl --addr localhost:4000 status
./mimorictl --addr localhost:4002 status

# Start Node 3
echo -e "\n${GREEN}Starting Node 3 on :4004...${NC}"
MIMORI_ADDR=:4004 MIMORI_NODE_ID=localhost:4004 MIMORI_DATA=./test-data-node3 MIMORI_PEERS=localhost:4000 ./mimorid &
NODE3_PID=$!
sleep 3

# Verify Node 3 started
if ! kill -0 $NODE3_PID 2>/dev/null; then
    echo -e "${RED}Node 3 failed to start!${NC}"
    kill $NODE1_PID $NODE2_PID 2>/dev/null
    exit 1
fi

# Add Node 3 to cluster
echo -e "\n${BLUE}=== Test 2: Adding Node 3 to cluster ===${NC}"

# Find current leader again (might have changed)
echo -e "${YELLOW}Finding current leader...${NC}"
LEADER_ADDR=""
for port in 4000 4002; do
    if ./mimorictl --addr localhost:$port status 2>/dev/null | grep -q "State:.*leader"; then
        LEADER_ADDR="localhost:$port"
        echo -e "${GREEN}Leader is at $LEADER_ADDR${NC}"
        break
    fi
done

if [ -z "$LEADER_ADDR" ]; then
    echo -e "${RED}No leader found!${NC}"
    kill $NODE1_PID $NODE2_PID $NODE3_PID 2>/dev/null
    exit 1
fi

echo -e "${YELLOW}Adding localhost:4004 to cluster via leader at $LEADER_ADDR...${NC}"
if ! ./mimorictl --addr $LEADER_ADDR add-node localhost:4004; then
    echo -e "${RED}Failed to add node!${NC}"
    kill $NODE1_PID $NODE2_PID $NODE3_PID 2>/dev/null
    exit 1
fi
sleep 2

# Test data replication with 3 nodes
echo -e "\n${BLUE}=== Test 3: Data replication with 3 nodes ===${NC}"

# Find current leader first
LEADER_ADDR=""
for port in 4000 4002 4004; do
    if ./mimorictl --addr localhost:$port status 2>/dev/null | grep -q "State:.*leader"; then
        LEADER_ADDR="localhost:$port"
        break
    fi
done

echo -e "${YELLOW}Writing test data via leader at $LEADER_ADDR...${NC}"
./mimorictl --addr $LEADER_ADDR put test-key "test-value-with-3-nodes"
sleep 2

# Verify data on all nodes with retries
echo -e "${YELLOW}Verifying data replication...${NC}"
for port in 4000 4002 4004; do
    echo -n "Node $port: "
    for attempt in {1..3}; do
        result=$(./mimorictl --addr localhost:$port get test-key 2>/dev/null)
        if [ -n "$result" ] && [ "$result" != "(nil)" ]; then
            echo "$result"
            break
        fi
        if [ $attempt -eq 3 ]; then
            echo "not available (tried 3 times)"
        else
            sleep 1
        fi
    done
done

# Remove Node 3
echo -e "\n${BLUE}=== Test 4: Removing Node 3 from cluster ===${NC}"

# Find current leader
echo -e "${YELLOW}Finding current leader...${NC}"
LEADER_ADDR=""
for port in 4000 4002 4004; do
    if ./mimorictl --addr localhost:$port status 2>/dev/null | grep -q "State:.*leader"; then
        LEADER_ADDR="localhost:$port"
        echo -e "${GREEN}Leader is at $LEADER_ADDR${NC}"
        break
    fi
done

if [ -z "$LEADER_ADDR" ]; then
    echo -e "${RED}No leader found!${NC}"
    kill $NODE1_PID $NODE2_PID $NODE3_PID 2>/dev/null
    exit 1
fi

echo -e "${YELLOW}Removing localhost:4004 from cluster via leader at $LEADER_ADDR...${NC}"
if ! ./mimorictl --addr $LEADER_ADDR remove-node localhost:4004; then
    echo -e "${RED}Failed to remove node!${NC}"
    kill $NODE1_PID $NODE2_PID $NODE3_PID 2>/dev/null
    exit 1
fi

# Stop Node 3 since it's been removed and will spam elections
if kill -0 $NODE3_PID 2>/dev/null; then
    echo -e "${YELLOW}Stopping removed Node 3...${NC}"
    kill $NODE3_PID 2>/dev/null
    sleep 1
fi
sleep 2

# Stop Node 3 after it has been removed to avoid stray election spam
if kill -0 $NODE3_PID 2>/dev/null; then
    kill $NODE3_PID 2>/dev/null
    wait $NODE3_PID 2>/dev/null
fi

# Verify Node 3 is removed
echo -e "\n${YELLOW}Checking cluster status after removal...${NC}"
./mimorictl --addr localhost:4000 status
./mimorictl --addr localhost:4002 status

# Test data still works with 2 nodes
echo -e "\n${BLUE}=== Test 5: Data operations with 2 nodes ===${NC}"
./mimorictl --addr localhost:4000 put test-key2 "value-with-2-nodes"
sleep 1
./mimorictl --addr localhost:4000 get test-key2
./mimorictl --addr localhost:4002 get test-key2

# Try to remove the last peer (should fail)
echo -e "\n${BLUE}=== Test 6: Attempting to remove last peer (should fail) ===${NC}"
if ./mimorictl --addr localhost:4000 remove-node localhost:4002 2>&1 | grep -q "cannot remove last peer"; then
    echo -e "${GREEN}✓ Correctly rejected removal of last peer${NC}"
else
    echo -e "${RED}✗ Should have rejected removal of last peer${NC}"
fi

echo -e "\n${GREEN}=== All tests completed! ===${NC}"
echo -e "${YELLOW}Nodes are still running. Press Ctrl+C to stop them, or run:${NC}"
echo -e "kill $NODE1_PID $NODE2_PID $NODE3_PID"
echo -e "rm -rf ./test-data-node{1,2,3,4}"

# Wait for user to stop
trap "echo -e '\n${YELLOW}Stopping nodes...${NC}'; kill $NODE1_PID $NODE2_PID $NODE3_PID 2>/dev/null; rm -rf ./test-data-node{1,2,3,4}; exit" INT
wait

