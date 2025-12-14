#!/bin/bash
#
# Test script for leader transfer functionality.
#
set -e

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

echo -e "${BLUE}=== Leader Transfer Test ===${NC}\n"

echo -e "${YELLOW}Building binaries...${NC}"
go build -o bin/mimorid ./cmd/mimorid
go build -o bin/mimorictl ./cmd/mimorictl

echo -e "${YELLOW}Cleanup...${NC}"
pkill -f "mimorid" 2>/dev/null || true
sleep 1
lsof -ti:4000,4002,4004 2>/dev/null | xargs kill -9 2>/dev/null || true
rm -rf ./test-data-transfer-*

echo -e "\n${GREEN}Starting 3-node cluster...${NC}"

MIMORI_ADDR=:4000 MIMORI_NODE_ID=localhost:4000 MIMORI_DATA=./test-data-transfer-node1 ./bin/mimorid &
NODE1_PID=$!
sleep 3

echo -e "${YELLOW}Waiting for Node 1 to become leader...${NC}"
for i in {1..20}; do
  if ./bin/mimorictl --addr localhost:4000 status 2>/dev/null | grep -q "State:.*leader"; then
    echo -e "${GREEN}Node 1 is leader!${NC}"
    break
  fi
  sleep 0.5
done

MIMORI_ADDR=:4002 MIMORI_NODE_ID=localhost:4002 MIMORI_DATA=./test-data-transfer-node2 MIMORI_PEERS=localhost:4000 ./bin/mimorid &
NODE2_PID=$!
sleep 2
./bin/mimorictl --addr localhost:4000 add-node localhost:4002
sleep 1

MIMORI_ADDR=:4004 MIMORI_NODE_ID=localhost:4004 MIMORI_DATA=./test-data-transfer-node3 MIMORI_PEERS=localhost:4000 ./bin/mimorid &
NODE3_PID=$!
sleep 2
./bin/mimorictl --addr localhost:4000 add-node localhost:4004
sleep 1

echo -e "\n${BLUE}=== Transfer leadership ===${NC}"

LEADER=""
for port in 4000 4002 4004; do
  if ./bin/mimorictl --addr localhost:$port status 2>/dev/null | grep -q "State:.*leader"; then
    LEADER="localhost:$port"
    break
  fi
done
if [ -z "$LEADER" ]; then
  echo -e "${RED}No leader found${NC}"
  exit 1
fi

TARGET=""
if [ "$LEADER" == "localhost:4000" ]; then
  TARGET="localhost:4002"
elif [ "$LEADER" == "localhost:4002" ]; then
  TARGET="localhost:4004"
else
  TARGET="localhost:4000"
fi

echo -e "${YELLOW}Transferring leadership from $LEADER to $TARGET...${NC}"
./bin/mimorictl --addr $LEADER transfer-leadership $TARGET
sleep 3

NEW_LEADER=""
for port in 4000 4002 4004; do
  if ./bin/mimorictl --addr localhost:$port status 2>/dev/null | grep -q "State:.*leader"; then
    NEW_LEADER="localhost:$port"
    break
  fi
done

if [ "$NEW_LEADER" == "$TARGET" ]; then
  echo -e "${GREEN}✓ Leadership transfer successful! New leader: $NEW_LEADER${NC}"
else
  echo -e "${RED}✗ Leadership transfer failed. Expected $TARGET, got ${NEW_LEADER:-none}${NC}"
  exit 1
fi

echo -e "\n${BLUE}=== Data ops after transfer ===${NC}"
./bin/mimorictl --addr $NEW_LEADER put transfer-test "test-value"
sleep 1
result=$(./bin/mimorictl --addr $NEW_LEADER get transfer-test 2>/dev/null || true)
if [ "$result" == "test-value" ]; then
  echo -e "${GREEN}✓ OK${NC}"
else
  echo -e "${RED}✗ FAILED${NC}"
fi

echo -e "\n${YELLOW}Cleaning up...${NC}"
kill $NODE1_PID $NODE2_PID $NODE3_PID 2>/dev/null || true
rm -rf ./test-data-transfer-*


