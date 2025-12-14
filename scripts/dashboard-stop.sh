#!/bin/bash
#
# Stop local Mimori dashboard node (default ports :4000/:4001, data-dashboard).
#
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

echo "Stopping Mimori dashboard..."

# Best-effort: kill any mimorid using dashboard data dir
pkill -f "data-dashboard" 2>/dev/null || true

# Also try to kill anything still bound to the default ports
lsof -ti:4000 2>/dev/null | xargs kill -9 2>/dev/null || true
lsof -ti:4001 2>/dev/null | xargs kill -9 2>/dev/null || true

sleep 1

if pgrep -f "bin/mimorid.*data-dashboard" > /dev/null 2>&1; then
  echo "[FAIL] Could not stop server. Try: pkill -9 -f mimorid"
  exit 1
fi

echo "[OK] Server stopped"


