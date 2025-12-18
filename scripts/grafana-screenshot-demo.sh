#!/usr/bin/env bash
set -euo pipefail

# Grafana Screenshot Demo
#
# Generates meaningful activity for the Mimori Grafana dashboard:
# - KV traffic (Put/Get) to move proposals/applied/commit graphs
# - Optional leader failover to show term/leader changes and nodes-up dip
#
# Usage:
#   bash scripts/grafana-screenshot-demo.sh
#
# Optional env vars:
#   ITERATIONS=2000        # number of keys to write/read (default: 2000)
#   KEY_PREFIX=demo        # key prefix (default: demo)
#   FAILOVER=1             # stop/start the current leader container (default: 1)
#   FAILOVER_SLEEP=8       # seconds to keep leader stopped (default: 8)
#   SNAPSHOT=1             # force a snapshot after traffic (default: 1)
#   SKIP_UP=1              # don't run docker-compose up -d (default: 0)

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

ITERATIONS="${ITERATIONS:-2000}"
KEY_PREFIX="${KEY_PREFIX:-demo}"
FAILOVER="${FAILOVER:-1}"
FAILOVER_SLEEP="${FAILOVER_SLEEP:-8}"
SNAPSHOT="${SNAPSHOT:-1}"
SKIP_UP="${SKIP_UP:-0}"

pick_compose() {
  if command -v docker-compose >/dev/null 2>&1; then
    echo "docker-compose"
    return
  fi
  if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    echo "docker compose"
    return
  fi
  echo "missing docker compose (docker-compose or docker compose)" >&2
  exit 1
}

COMPOSE="$(pick_compose)"

pick_mimorictl() {
  if command -v mimorictl >/dev/null 2>&1; then
    echo "mimorictl"
    return
  fi
  if [[ -x "${ROOT_DIR}/bin/mimorictl" ]]; then
    echo "${ROOT_DIR}/bin/mimorictl"
    return
  fi
  if [[ -x "${ROOT_DIR}/mimorictl" ]]; then
    echo "${ROOT_DIR}/mimorictl"
    return
  fi
  # Fallback: go run (slower but works without install/build)
  echo "go run ${ROOT_DIR}/cmd/mimorictl"
}

MIMORICTL="$(pick_mimorictl)"

log() {
  printf '%s\n' "$*" >&2
}

run_mimorictl() {
  # shellcheck disable=SC2086
  ${MIMORICTL} "$@"
}

leader_id() {
  # Output is like:
  #   Leader ID: mimori-node1:4000
  #   This node (...) is the leader
  # We parse the first "Leader ID:" line.
  run_mimorictl leader 2>/dev/null | awk -F': ' '/^Leader ID:/ {print $2; exit}'
}

compose_stop_start_leader() {
  local id svc
  id="$(leader_id || true)"
  if [[ -z "${id}" ]]; then
    log "Could not determine leader ID; skipping failover."
    return
  fi

  # In docker-compose mode, leader IDs look like "mimori-node1:4000" and the service is "mimori-node1".
  if [[ "${id}" =~ ^mimori-node[0-9]+: ]]; then
    svc="${id%%:*}"
    log "Failover: stopping leader service ${svc} for ${FAILOVER_SLEEP}s (leader_id=${id})"
    # shellcheck disable=SC2086
    ${COMPOSE} stop "${svc}" >/dev/null
    sleep "${FAILOVER_SLEEP}"
    log "Failover: starting leader service ${svc}"
    # shellcheck disable=SC2086
    ${COMPOSE} start "${svc}" >/dev/null
    return
  fi

  log "Leader ID (${id}) does not look like docker-compose service; skipping failover."
}

main() {
  log "Using compose: ${COMPOSE}"
  log "Using mimorictl: ${MIMORICTL}"

  if [[ "${SKIP_UP}" != "1" ]]; then
    log "Ensuring docker stack is up..."
    # shellcheck disable=SC2086
    ${COMPOSE} up -d >/dev/null
  fi

  log "Generating KV traffic: ${ITERATIONS} put/get ops"
  for i in $(seq 1 "${ITERATIONS}"); do
    # Keep this simple/portable (no parallelism).
    run_mimorictl put "${KEY_PREFIX}-k${i}" "v${i}" >/dev/null
    run_mimorictl get "${KEY_PREFIX}-k${i}" >/dev/null || true
  done

  if [[ "${SNAPSHOT}" == "1" ]]; then
    log "Forcing snapshot on leader..."
    # This hits POST /raft/snapshot on the current leader.
    run_mimorictl snapshot >/dev/null || true
  fi

  if [[ "${FAILOVER}" == "1" ]]; then
    compose_stop_start_leader
  fi

  log ""
  log "Now open Grafana: http://localhost:3000 (admin/admin)"
  log "Suggested time range: Last 15m, refresh: 5s"
  log "Take the screenshot while proposals/applied are elevated and leader/term shows the failover."
}

main "$@"


