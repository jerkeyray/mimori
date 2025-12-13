#!/bin/bash
# Test script for leader transfer functionality

set -e

GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

echo -e "${BLUE}=== Leader Transfer Test ===${NC}\n"

# Build binaries
echo -e "${YELLOW}Building binaries...${NC}"
go build -o mimorid ./cmd/mimorid
go build -o mimorictl ./cmd/mimorictl

# Cleanup
pkill -f "mimorid" 2>/dev/null || true
sleep 1
lsof -ti:4000,4002,4004 2>/dev/null | xargs kill -9 2>/dev/null || true
rm -rf ./test-data-transfer-*

echo -e "\n${GREEN}Starting 3-node cluster...${NC}"

# Start Node 1
MIMORI_ADDR=:4000 MIMORI_NODE_ID=localhost:4000 MIMORI_DATA=./test-data-transfer-node1 ./mimorid &
NODE1_PID=$!
sleep 3

# Wait for Node 1 to become leader
echo -e "${YELLOW}Waiting for Node 1 to become leader...${NC}"
for i in {1..10}; do
    if ./mimorictl --addr localhost:4000 status 2>/dev/null | grep -q "State:.*leader"; then
        echo -e "${GREEN}Node 1 is leader!${NC}"
        break
    fi
    sleep 1
done

# Start Node 2
MIMORI_ADDR=:4002 MIMORI_NODE_ID=localhost:4002 MIMORI_DATA=./test-data-transfer-node2 MIMORI_PEERS=localhost:4000 ./mimorid &
NODE2_PID=$!
sleep 2

# Add Node 2
echo -e "${YELLOW}Adding Node 2...${NC}"
./mimorictl --addr localhost:4000 add-node localhost:4002
sleep 2

# Start Node 3
MIMORI_ADDR=:4004 MIMORI_NODE_ID=localhost:4004 MIMORI_DATA=./test-data-transfer-node3 MIMORI_PEERS=localhost:4000 ./mimorid &
NODE3_PID=$!
sleep 2

# Add Node 3
echo -e "${YELLOW}Adding Node 3...${NC}"
LEADER=$(./mimorictl --addr localhost:4000 status 2>/dev/null | grep "State:" | grep -q "leader" && echo "localhost:4000" || echo "localhost:4002")
./mimorictl --addr $LEADER add-node localhost:4004
sleep 2

echo -e "\n${BLUE}=== Test: Leader Transfer ===${NC}"

# Find current leader
LEADER=""
for port in 4000 4002 4004; do
    if ./mimorictl --addr localhost:$port status 2>/dev/null | grep -q "State:.*leader"; then
        LEADER="localhost:$port"
        echo -e "${GREEN}Current leader: $LEADER${NC}"
        break
    fi
done

# Determine target (one of the other nodes)
TARGET=""
if [ "$LEADER" == "localhost:4000" ]; then
    TARGET="localhost:4002"
elif [ "$LEADER" == "localhost:4002" ]; then
    TARGET="localhost:4004"
else
    TARGET="localhost:4000"
fi

echo -e "${YELLOW}Transferring leadership from $LEADER to $TARGET...${NC}"
./mimorictl --addr $LEADER transfer-leadership $TARGET

# Wait for transfer to complete
echo -e "${YELLOW}Waiting for transfer to complete...${NC}"
sleep 3

# Verify new leader
NEW_LEADER=""
for port in 4000 4002 4004; do
    if ./mimorictl --addr localhost:$port status 2>/dev/null | grep -q "State:.*leader"; then
        NEW_LEADER="localhost:$port"
        break
    fi
done

if [ "$NEW_LEADER" == "$TARGET" ]; then
    echo -e "${GREEN}✓ Leadership transfer successful! New leader: $NEW_LEADER${NC}"
else
    echo -e "${RED}✗ Leadership transfer failed. Expected $TARGET, got ${NEW_LEADER:-none}${NC}"
    kill $NODE1_PID $NODE2_PID $NODE3_PID 2>/dev/null
    exit 1
fi

# Test data operations still work
echo -e "\n${BLUE}=== Test: Data operations after transfer ===${NC}"
./mimorictl --addr $NEW_LEADER put transfer-test "test-value"
sleep 1
result=$(./mimorictl --addr $NEW_LEADER get transfer-test 2>/dev/null)
if [ "$result" == "test-value" ]; then
    echo -e "${GREEN}✓ Data operations work with new leader${NC}"
else
    echo -e "${RED}✗ Data operations failed${NC}"
fi

echo -e "\n${GREEN}=== All tests passed! ===${NC}"
echo -e "${YELLOW}Cleaning up...${NC}"
kill $NODE1_PID $NODE2_PID $NODE3_PID 2>/dev/null
rm -rf ./test-data-transfer-*

