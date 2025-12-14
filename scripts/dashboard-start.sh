#!/bin/bash
#
# Start a single-node Mimori + serve the web dashboard.
#
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

echo "Mimori Dashboard"
echo "================"
echo ""

echo "[1/4] Building mimorid..."
go build -o bin/mimorid ./cmd/mimorid
echo "  [OK] Build complete"
echo ""

echo "[2/4] Cleaning old processes..."
pkill -f "bin/mimorid" 2>/dev/null || true
sleep 1
echo "  [OK] Cleanup complete"
echo ""

echo "[3/4] Cleaning old data..."
rm -rf ./data-dashboard ./data-spawn >/dev/null 2>&1 || true
echo "  [OK] Data cleaned"
echo ""

echo "[4/4] Starting Mimori node..."
MIMORI_ADDR=:4000 MIMORI_NODE_ID=:4000 MIMORI_PEERS="" MIMORI_DATA=./data-dashboard \
  ./bin/mimorid > /tmp/mimorid-dashboard.log 2>&1 &
SERVER_PID=$!
echo "  [OK] Server started (PID: $SERVER_PID)"

echo "Waiting for HTTP to be ready..."
for i in {1..20}; do
  if curl -s http://localhost:4001/healthz > /dev/null 2>&1; then
    echo "  [OK] Server is ready!"
    break
  fi
  if [ $i -eq 20 ]; then
    echo "  [FAIL] Server did not start in time"
    kill $SERVER_PID 2>/dev/null || true
    exit 1
  fi
  sleep 0.5
done
echo ""

echo "Dashboard: http://localhost:4001/dashboard/"
echo "Logs:      /tmp/mimorid-dashboard.log"
echo ""
echo "Stop: bash scripts/dashboard-stop.sh"
echo ""

if command -v open > /dev/null; then
  open http://localhost:4001/dashboard/ 2>/dev/null || true
elif command -v xdg-open > /dev/null; then
  xdg-open http://localhost:4001/dashboard/ 2>/dev/null || true
fi

trap "echo ''; echo 'Stopping server...'; kill $SERVER_PID 2>/dev/null || true; echo 'Server stopped.'; exit 0" INT TERM
wait $SERVER_PID


