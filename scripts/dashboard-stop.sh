#!/bin/bash
#
# Stop local Mimori dashboard node (default ports :4000/:4001, data-dashboard).
#
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

echo "Stopping Mimori dashboard..."

# If we have a pid file from dashboard-start.sh, use it (supports custom ports).
for pidfile in /tmp/mimorid-dashboard-*.pid; do
  if [ -f "$pidfile" ]; then
    pid="$(cat "$pidfile" 2>/dev/null || true)"
    if [ -n "${pid}" ]; then
      kill "${pid}" 2>/dev/null || true
    fi
    rm -f "$pidfile" 2>/dev/null || true
  fi
done

# Best-effort: kill any mimorid using dashboard data dir(s)
pkill -f "data-dashboard-" 2>/dev/null || true

# Also try to kill anything still bound to common dashboard ports
for p in 4000 4001 4100 4101 4200 4201; do
  lsof -ti:"$p" 2>/dev/null | xargs kill -9 2>/dev/null || true
done

sleep 1

if pgrep -f "bin/mimorid.*data-dashboard-" > /dev/null 2>&1; then
  echo "[FAIL] Could not stop server. Try: pkill -9 -f mimorid"
  exit 1
fi

echo "[OK] Server stopped"


