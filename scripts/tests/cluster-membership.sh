#!/bin/bash
#
# Test script for dynamic cluster membership.
#
set -e

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

# Colors for output
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

echo -e "${BLUE}=== Mimori Dynamic Cluster Membership Test ===${NC}\n"

echo -e "${YELLOW}Building binaries...${NC}"
go build -o bin/mimorid ./cmd/mimorid
go build -o bin/mimorictl ./cmd/mimorictl
echo -e "${GREEN}✓ Binaries built successfully${NC}\n"

echo -e "${YELLOW}Cleaning up old processes...${NC}"
pkill -f "mimorid" 2>/dev/null || true
sleep 1

for port in 4000 4002 4004; do
  if lsof -ti:$port > /dev/null 2>&1; then
    echo -e "${RED}Port $port is still in use; killing process...${NC}"
    lsof -ti:$port | xargs kill -9 2>/dev/null || true
    sleep 1
  fi
done

echo -e "${YELLOW}Cleaning up old data...${NC}"
rm -rf ./test-data-node{1,2,3,4}

echo -e "\n${GREEN}Starting Node 1 on :4000...${NC}"
MIMORI_ADDR=:4000 MIMORI_NODE_ID=localhost:4000 MIMORI_DATA=./test-data-node1 ./bin/mimorid &
NODE1_PID=$!
sleep 3

if ! kill -0 $NODE1_PID 2>/dev/null; then
  echo -e "${RED}Node 1 failed to start!${NC}"
  exit 1
fi

echo -e "${YELLOW}Waiting for Node 1 to become leader...${NC}"
for i in {1..20}; do
  if ./bin/mimorictl --addr localhost:4000 status 2>/dev/null | grep -q "leader"; then
    echo -e "${GREEN}Node 1 is leader!${NC}"
    break
  fi
  if [ $i -eq 20 ]; then
    echo -e "${RED}Node 1 failed to become leader.${NC}"
    kill $NODE1_PID 2>/dev/null || true
    exit 1
  fi
  sleep 0.5
done

echo -e "\n${GREEN}Starting Node 2 on :4002...${NC}"
MIMORI_ADDR=:4002 MIMORI_NODE_ID=localhost:4002 MIMORI_DATA=./test-data-node2 MIMORI_PEERS=localhost:4000 ./bin/mimorid &
NODE2_PID=$!
sleep 2

echo -e "\n${BLUE}=== Test 1: Adding Node 2 to cluster ===${NC}"
if ! ./bin/mimorictl --addr localhost:4000 add-node localhost:4002; then
  echo -e "${RED}Failed to add node 2${NC}"
  kill $NODE1_PID $NODE2_PID 2>/dev/null || true
  exit 1
fi
sleep 1

echo -e "\n${GREEN}Starting Node 3 on :4004...${NC}"
MIMORI_ADDR=:4004 MIMORI_NODE_ID=localhost:4004 MIMORI_DATA=./test-data-node3 MIMORI_PEERS=localhost:4000 ./bin/mimorid &
NODE3_PID=$!
sleep 2

echo -e "\n${BLUE}=== Test 2: Adding Node 3 to cluster ===${NC}"
if ! ./bin/mimorictl --addr localhost:4000 add-node localhost:4004; then
  echo -e "${RED}Failed to add node 3${NC}"
  kill $NODE1_PID $NODE2_PID $NODE3_PID 2>/dev/null || true
  exit 1
fi
sleep 1

echo -e "\n${BLUE}=== Test 3: Data replication ===${NC}"
./bin/mimorictl --addr localhost:4000 put test-key "test-value"
sleep 1
for port in 4000 4002 4004; do
  echo -n "Node $port get: "
  ./bin/mimorictl --addr localhost:$port get test-key || true
done

echo -e "\n${BLUE}=== Test 4: Removing Node 3 ===${NC}"
./bin/mimorictl --addr localhost:4000 remove-node localhost:4004 || true
sleep 1

echo -e "\n${GREEN}Done. Cleaning up...${NC}"
kill $NODE1_PID $NODE2_PID $NODE3_PID 2>/dev/null || true
rm -rf ./test-data-node{1,2,3,4}


