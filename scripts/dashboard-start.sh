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

BASE_PORT="${MIMORI_BASE_PORT:-4000}"
HTTP_PORT="$((BASE_PORT + 1))"
DATA_DIR="./data-dashboard-${BASE_PORT}"
LOG_FILE="/tmp/mimorid-dashboard-${BASE_PORT}.log"
PID_FILE="/tmp/mimorid-dashboard-${BASE_PORT}.pid"

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
rm -rf "${DATA_DIR}" ./data-spawn >/dev/null 2>&1 || true
echo "  [OK] Data cleaned"
echo ""

echo "[4/4] Starting Mimori node..."
if lsof -ti:"${BASE_PORT}" >/dev/null 2>&1 || lsof -ti:"${HTTP_PORT}" >/dev/null 2>&1; then
  echo "  [FAIL] Port ${BASE_PORT}/${HTTP_PORT} already in use."
  echo "         If Docker Compose is running, stop it with: docker-compose down"
  echo "         Or run on a different port: MIMORI_BASE_PORT=4100 bash scripts/dashboard-start.sh"
  exit 1
fi

MIMORI_ADDR=":${BASE_PORT}" MIMORI_NODE_ID="localhost:${BASE_PORT}" MIMORI_PEERS="" MIMORI_DATA="${DATA_DIR}" \
  ./bin/mimorid > "${LOG_FILE}" 2>&1 &
SERVER_PID=$!
echo "${SERVER_PID}" > "${PID_FILE}"
echo "  [OK] Server started (PID: $SERVER_PID)"

echo "Waiting for HTTP to be ready..."
for i in {1..20}; do
  if curl -s "http://localhost:${HTTP_PORT}/healthz" > /dev/null 2>&1; then
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

echo "Dashboard: http://localhost:${HTTP_PORT}/dashboard/"
echo "Logs:      ${LOG_FILE}"
echo ""
echo "Stop: bash scripts/dashboard-stop.sh"
echo ""

if command -v open > /dev/null; then
  open "http://localhost:${HTTP_PORT}/dashboard/" 2>/dev/null || true
elif command -v xdg-open > /dev/null; then
  xdg-open "http://localhost:${HTTP_PORT}/dashboard/" 2>/dev/null || true
fi

trap "echo ''; echo 'Stopping server...'; kill $SERVER_PID 2>/dev/null || true; echo 'Server stopped.'; exit 0" INT TERM
wait $SERVER_PID


