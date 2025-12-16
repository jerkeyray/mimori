## Mimori — Distributed Key-Value Store (Raft)

Mimori is a distributed key-value store built in Go on top of the Raft consensus algorithm. It provides a strongly-consistent write path through the Raft leader, optional follower reads (bounded staleness), dynamic cluster membership, and an ops-friendly HTTP surface (health, status, metrics, and a web dashboard).

## Features

- **Raft consensus**: leader election, log replication, snapshots
- **Strong writes**: `Put`/`Delete` are committed through the leader
- **Follower reads (optional)**: `Get --allow-stale` can be served by followers with a read-lease
- **Dynamic membership**: add/remove nodes at runtime via Raft config changes
- **Leader transfer**: graceful handoff for maintenance
- **Persistent storage**: Pebble-backed KV state machine
- **Ops HTTP endpoints**: `/healthz`, `/ready`, `/raft/status`, `/raft/snapshot`, `/metrics`
- **Web dashboard**: embedded UI served by each node on its HTTP port (includes REST API for cluster + KV)
- **CLI admin tool**: `mimorictl` for KV and cluster operations with leader discovery + retries

## Quick start (Docker Compose)

Start a 3-node cluster + Prometheus + Grafana:

```bash
docker-compose up -d

docker-compose ps
```

Ports (per node):

- **Node 1**: gRPC `localhost:4000`, HTTP `localhost:4001` (dashboard at `http://localhost:4001/`)
- **Node 2**: gRPC `localhost:4002`, HTTP `localhost:4003` (dashboard at `http://localhost:4003/`)
- **Node 3**: gRPC `localhost:4004`, HTTP `localhost:4005` (dashboard at `http://localhost:4005/`)
- **Prometheus**: `http://localhost:9090`
- **Grafana**: `http://localhost:3000` (admin/admin)

Stop:

```bash
docker-compose down
```

### Build and use the CLI

You do **not** have to use a local `./bin` directory if you don’t want to. Use the normal Go workflow:

1. **Install the CLI into your Go bin directory:**

   ```bash
   # from the repo root
   go install ./cmd/mimorictl

   # make sure Go's bin dir is on your PATH (usually ~/go/bin)
   export PATH="$(go env GOPATH)/bin:$PATH"
   ```

2. **Use the CLI directly:**

   ```bash
   mimorictl put key1 value1
   mimorictl get key1
   ```

   When you change CLI code or pull new changes, just run `go install ./cmd/mimorictl` again; the binary on your PATH will be updated.

If you prefer keeping binaries inside the repo (for local dev), you can still do:

```bash
go build -o bin/mimorictl ./cmd/mimorictl
./bin/mimorictl put key1 value1
```

#### CLI addressing (seeds, env vars, and aliases)

- **Leader discovery via seeds**

  The CLI discovers the leader by talking to one or more seed nodes:

  - **Env-based seeds (nice for day-to-day use with Docker Compose):**

    ```bash
    export MIMORI_ADDRS=127.0.0.1:4000,127.0.0.1:4002,127.0.0.1:4004

    mimorictl p key1 value1   # uses MIMORI_ADDRS, no --addr needed
    mimorictl g key1
    ```

  - **`--addr` flag (overrides env):**

    ```bash
    mimorictl --addr 127.0.0.1:4000,127.0.0.1:4002 status
    ```

- **Short aliases for common commands**

  ```bash
  # KV
  mimorictl p key value        # put
  mimorictl g key              # get
  mimorictl d key              # del

  # Admin
  mimorictl h                  # health
  mimorictl st                 # status
  mimorictl m                  # metrics
  mimorictl ldr                # leader

  # Cluster management
  mimorictl add :4002          # add-node :4002
  mimorictl rm :4002           # remove-node :4002
  mimorictl tl :4002           # transfer-leadership :4002
  ```

### Using the dashboard in containers

Each node serves the dashboard on its HTTP port (which is **gRPC port + 1**). Open any of:

- `http://localhost:4001/`
- `http://localhost:4003/`
- `http://localhost:4005/`

The root (`/`) redirects to `/dashboard/`.

## Quick start (local single-node dashboard)

For a simple local run (no Docker), use the helper script:

```bash
bash scripts/dashboard-start.sh
```

It starts a single node on `:4000` and prints the dashboard URL (`http://localhost:4001/dashboard/`).

Stop it with:

```bash
bash scripts/dashboard-stop.sh
```

## Architecture

### High-level diagram

```text
┌─────────────┐
│   Clients   │
│ (CLI / UI)  │
└──────┬──────┘
       │ gRPC (KV + Raft admin)
       ▼
┌─────────────┐     ┌─────────────┐     ┌─────────────┐
│   Node 1    │◄───►│   Node 2    │◄───►│   Node 3    │
│  (Leader)   │     │  (Follower) │     │  (Follower) │
└──────┬──────┘     └──────┬──────┘     └──────┬──────┘
       │                    │                    │
       │ apply committed log │ apply committed log │ apply committed log
       ▼                    ▼                    ▼
┌─────────────┐       ┌─────────────┐       ┌─────────────┐
│  Pebble KV  │       │  Pebble KV  │       │  Pebble KV  │
└─────────────┘       └─────────────┘       └─────────────┘

(Each node also exposes an HTTP port = gRPC port + 1 for health/status/metrics + dashboard.)
```

### What runs inside a node

- **Raft core** (`internal/raft/`)
  - Elections, heartbeats, replication, snapshots
  - Dynamic membership (config change log entries)
  - Leader transfer
  - Read lease tracking for follower reads
- **State machine / storage** (`internal/storage/`)
  - Pebble-backed KV store
  - Apply loop consumes committed log entries and mutates the KV store
- **gRPC API** (`internal/api/`)
  - KV service: `Put/Get/Delete/Health`
  - Raft admin service: membership, leader transfer, etc.
- **HTTP server** (port = gRPC + 1)
  - Ops endpoints: `/healthz`, `/ready`, `/raft/status`, `/raft/snapshot`, `/metrics`
  - Embedded dashboard UI at `/dashboard/` plus REST endpoints under `/api/...`

### Consistency model

- **Writes (`Put`, `Delete`)**

  - Must be handled by the Raft leader.
  - Followers reject writes with a `leader=...` hint; the dashboard proxies writes to the leader.

- **Reads (`Get`)**
  - Default is **leader reads** (strong / linearizable in the usual Raft sense).
  - Optional **follower reads** are allowed only when:
    - the client opts in (`--allow-stale` in CLI or `allow_stale=true` in the dashboard REST API), and
    - the follower has a **valid read lease** (recent heartbeat).

See `docs/FOLLOWER_READS.md` for the full explanation.

## Interfaces

### CLI (`mimorictl`)

Build:

```bash
go build -o bin/mimorictl ./cmd/mimorictl
```

Commands:

```bash
# KV
mimorictl put <key> <value>
mimorictl get <key>
mimorictl get <key> --allow-stale
mimorictl del <key>

# Ops / admin
mimorictl health
mimorictl status
mimorictl leader
mimorictl metrics
mimorictl snapshot

# Cluster management (leader operations)
mimorictl add-node <node-id>
mimorictl remove-node <node-id>
mimorictl transfer-leadership <node-id>

# Seed nodes (comma-separated)
mimorictl --addr 127.0.0.1:4002,127.0.0.1:4000 status
```

### HTTP endpoints (per node)

Each node exposes HTTP on `gRPC_PORT + 1`:

- **Health**: `GET /healthz`
- **Readiness**: `GET /ready`
- **Raft status**: `GET /raft/status`
- **Force snapshot**: `POST /raft/snapshot` (leader only)
- **Prometheus**: `GET /metrics`
- **Dashboard**: `GET /` (redirects to `/dashboard/`)

### Dashboard REST API

These endpoints are used by the dashboard UI (all on the node’s HTTP port):

- **KV**:
  - `GET /api/kv/{key}`
  - `GET /api/kv/{key}?allow_stale=true`
  - `PUT /api/kv/{key}` with JSON `{ "value": "..." }`
  - `DELETE /api/kv/{key}`
- **Cluster**:
  - `GET /api/cluster/nodes`
  - `GET /api/cluster/status`
  - `POST /api/cluster/add-node`
  - `POST /api/cluster/remove-node`
  - `POST /api/cluster/transfer-leadership`
  - `POST /api/cluster/spawn-node` (best-effort local spawning; mainly for local/dev)
- **Dashboard status**:
  - `GET /api/status`

## Running `mimorid` manually

`mimorid` is configured via environment variables:

- **`MIMORI_ADDR`**: gRPC listen address (default `:4000`)
- **`MIMORI_DATA`**: data directory (default `data`)
- **`MIMORI_PEERS`**: comma-separated peer IDs/addresses (default empty)
- **`MIMORI_NODE_ID`**: Raft node ID (defaults to `MIMORI_ADDR`)
- **`MIMORI_LOG_FORMAT`**: `json` or `console` (default `console` outside Dockerfile)
- **`MIMORI_LOG_LEVEL`**: `debug|info|warn|error` (default `info`)

Example (single node):

```bash
MIMORI_ADDR=:4000 MIMORI_NODE_ID=:4000 MIMORI_PEERS="" MIMORI_DATA=./data1 ./bin/mimorid
```

Note: the HTTP server automatically binds on `:4001` when gRPC is `:4000`.

## Observability

- **Prometheus metrics**: scrape `/metrics` on each node’s HTTP port.
- **Grafana**: see `docker/` + `docker-compose.yml` for provisioning.

## Development

### Tests

See `docs/TESTING.md`.

### Repo layout

```text
cmd/                # binaries (mimorid, mimorictl)
internal/api/       # gRPC + HTTP layer (includes dashboard + REST API)
internal/raft/      # Raft implementation
internal/storage/   # Pebble wrapper
internal/cluster/   # peer monitoring/heartbeats
scripts/            # local helper scripts + smoke tests
tests/              # integration tests
```

## Troubleshooting

- **`not leader, leader=...`**: you hit a follower with a leader-only request. Use a seed list (`--addr a,b,c`) so the CLI can follow redirects.
- **Follower reads rejected**: use `--allow-stale` (CLI) or `allow_stale=true` (dashboard API) and ensure the follower has a valid lease.
- **Dashboard not loading**: open the node’s HTTP port and use `/` (redirects to `/dashboard/`).
