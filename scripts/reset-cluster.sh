#!/bin/bash
#
# Reset Mimori cluster data and stop local nodes.
#
# Usage:
#   bash scripts/reset-cluster.sh            # stop local processes + delete local data dirs
#   bash scripts/reset-cluster.sh --docker   # also docker compose down -v
#
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

WITH_DOCKER=0
if [ "${1:-}" = "--docker" ]; then
  WITH_DOCKER=1
fi

echo "Mimori Cluster Reset"
echo "===================="
echo ""

echo "[1/2] Stopping local mimorid processes..."
pkill -f mimorid 2>/dev/null || true
sleep 1
echo "  [OK] Done"
echo ""

echo "[2/2] Removing local data directories..."
rm -rf \
  ./data ./data1 ./data2 ./data3 \
  ./data-dashboard ./data-spawn \
  ./test-data-* ./test-data-node* ./test-data-transfer-* \
  >/dev/null 2>&1 || true
echo "  [OK] Done"
echo ""

if [ "$WITH_DOCKER" -eq 1 ]; then
  if command -v docker-compose > /dev/null 2>&1 && [ -f "docker-compose.yml" ]; then
    echo "Docker compose cleanup..."
    docker-compose down -v 2>/dev/null || true
    echo "  [OK] Docker containers/volumes removed"
  else
    echo "  [SKIP] docker-compose not available or docker-compose.yml missing"
  fi
fi


