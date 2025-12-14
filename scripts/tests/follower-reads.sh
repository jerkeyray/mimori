#!/bin/bash
#
# Smoke test for follower reads.
#
set -e

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

echo "Follower Reads Smoke Test"
echo "========================="
echo ""

go build -o bin/mimorid ./cmd/mimorid
go build -o bin/mimorictl ./cmd/mimorictl

pkill -f mimorid 2>/dev/null || true
sleep 1
lsof -ti:4000,4002,4004 2>/dev/null | xargs kill -9 2>/dev/null || true
rm -rf ./test-data-follower-reads-*

MIMORI_ADDR=:4000 MIMORI_NODE_ID=:4000 MIMORI_DATA=./test-data-follower-reads-1 ./bin/mimorid &
P1=$!
sleep 3

MIMORI_ADDR=:4002 MIMORI_NODE_ID=:4002 MIMORI_DATA=./test-data-follower-reads-2 MIMORI_PEERS=:4000 ./bin/mimorid &
P2=$!
sleep 2
./bin/mimorictl --addr localhost:4000 add-node :4002
sleep 1

MIMORI_ADDR=:4004 MIMORI_NODE_ID=:4004 MIMORI_DATA=./test-data-follower-reads-3 MIMORI_PEERS=:4000 ./bin/mimorid &
P3=$!
sleep 2
./bin/mimorictl --addr localhost:4000 add-node :4004
sleep 1

echo "Writing key via leader..."
./bin/mimorictl --addr localhost:4000 put test-key hello-world
sleep 1

echo "Leader read:"
./bin/mimorictl --addr localhost:4000 get test-key

echo "Follower read without --allow-stale (expected to fail):"
./bin/mimorictl --addr localhost:4002 get test-key || true

echo "Follower read with --allow-stale:"
./bin/mimorictl --addr localhost:4002 get test-key --allow-stale || true

echo ""
echo "Cleaning up..."
kill $P1 $P2 $P3 2>/dev/null || true
rm -rf ./test-data-follower-reads-*


