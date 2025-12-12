# Mimori - Distributed Key-Value Store

A distributed key-value store built with Raft consensus in Go.

## Features

- **Raft Consensus**: Leader election, log replication, and snapshotting
- **Distributed**: Multi-node cluster support with automatic leader election
- **Persistent Storage**: Built on PebbleDB for durability
- **gRPC API**: High-performance RPC interface
- **Observability**: Prometheus metrics and structured logging
- **Admin Tools**: CLI for cluster management

## Quick Start

### Using Docker Compose (Recommended)

```bash
# Start 3-node cluster with Prometheus and Grafana
docker-compose up -d

# Check status
docker-compose ps

# View logs
docker-compose logs -f mimori-node1

# Stop cluster
docker-compose down
```

Access:

- **Node 1**: `localhost:4000` (gRPC), `localhost:4001` (HTTP)
- **Node 2**: `localhost:4002` (gRPC), `localhost:4003` (HTTP)
- **Node 3**: `localhost:4004` (gRPC), `localhost:4005` (HTTP)
- **Prometheus**: `http://localhost:9090`
- **Grafana**: `http://localhost:3000` (admin/admin)

### Manual Setup

1. **Build binaries:**

   ```bash
   go build -o mimorid ./cmd/mimorid
   go build -o mimorictl ./cmd/mimorictl
   ```

2. **Start a node:**

   ```bash
   ./mimorid
   # Or with custom settings:
   MIMORI_ADDR=:4000 MIMORI_DATA=./data1 MIMORI_PEERS=:4001,:4002 ./mimorid
   ```

3. **Use the CLI:**
   ```bash
   ./mimorictl put key1 value1
   ./mimorictl get key1
   ./mimorictl status
   ```

## CLI Commands

```bash
# Key-Value Operations
mimorictl put <key> <value>    # Store a key-value pair
mimorictl get <key>             # Retrieve a value
mimorictl del <key>             # Delete a key

# Admin Commands
mimorictl status                # Show Raft status
mimorictl leader                # Show leader information
mimorictl snapshot              # Force snapshot creation (leader only)
mimorictl metrics               # Show key metrics
mimorictl health                # Health check

# Connect to specific node
mimorictl --addr localhost:4001 status
```

## HTTP Endpoints

Each node exposes HTTP endpoints on port `gRPC_PORT + 1`:

- `GET /healthz` - Health check (liveness)
- `GET /ready` - Readiness check
- `GET /raft/status` - Raft status (JSON)
- `POST /raft/snapshot` - Force snapshot (leader only)
- `GET /metrics` - Prometheus metrics

## Environment Variables

- `MIMORI_ADDR` - gRPC listen address (default: `:4000`)
- `MIMORI_DATA` - Data directory path (default: `data`)
- `MIMORI_PEERS` - Comma-separated peer addresses (default: empty)
- `MIMORI_LOG_FORMAT` - Log format: `json` or `console` (default: `console`)
- `MIMORI_LOG_LEVEL` - Log level: `debug`, `info`, `warn`, `error` (default: `info`)

## Architecture

```
┌─────────────┐
│   Client    │
└──────┬──────┘
       │ gRPC
       ▼
┌─────────────┐     ┌─────────────┐     ┌─────────────┐
│   Node 1    │◄───►│   Node 2    │◄───►│   Node 3    │
│  (Leader)   │     │  (Follower) │     │  (Follower) │
└──────┬──────┘     └─────────────┘     └─────────────┘
       │
       ▼
┌─────────────┐
│   PebbleDB  │
└─────────────┘
```

## Development

### Running Tests

```bash
# Unit tests
go test ./...

# E2E tests
go test ./tests
```

### Project Structure

```
.
├── cmd/
│   ├── mimorid/      # Main server binary
│   └── mimorictl/    # CLI client
├── internal/
│   ├── api/          # gRPC API server
│   ├── raft/         # Raft consensus implementation
│   ├── storage/      # PebbleDB wrapper
│   ├── cluster/      # Cluster membership
│   └── logging/      # Structured logging
├── proto/            # Protocol buffer definitions
├── docker/           # Docker configs (Prometheus, Grafana)
└── tests/            # Integration tests
```

## License

MIT
