# Docker Setup for Mimori

This directory contains Docker Compose configuration for running a 3-node Mimori cluster with Prometheus and Grafana.

## Quick Start

1. **Build and start the cluster:**

   ```bash
   docker-compose up -d
   ```

2. **Check cluster status:**

   ```bash
   docker-compose ps
   ```

3. **View logs:**

   ```bash
   # All nodes
   docker-compose logs -f

   # Specific node
   docker-compose logs -f mimori-node1
   ```

4. **Access services:**

   - **Node 1**: gRPC `localhost:4000`, HTTP `localhost:4001`
   - **Node 2**: gRPC `localhost:4002`, HTTP `localhost:4003`
   - **Node 3**: gRPC `localhost:4004`, HTTP `localhost:4005`
   - **Prometheus**: `http://localhost:9090`
   - **Grafana**: `http://localhost:3000` (admin/admin)

5. **Stop the cluster:**

   ```bash
   docker-compose down
   ```

6. **Stop and remove volumes (clean slate):**
   ```bash
   docker-compose down -v
   ```

## Using mimorictl

The CLI tool can connect to any node:

```bash
# Build mimorictl
go build -o mimorictl ./cmd/mimorictl

# Connect to node 1
./mimorictl --addr localhost:4000 put key1 value1
./mimorictl --addr localhost:4000 get key1

# Check status
./mimorictl --addr localhost:4000 status
./mimorictl --addr localhost:4000 leader
```

## Grafana Dashboard

1. Login to Grafana at `http://localhost:3000` (admin/admin)
2. Navigate to Dashboards → Import
3. The dashboard should auto-provision, or you can import `grafana-dashboard.json`

## Troubleshooting

- **Nodes not connecting**: Check logs with `docker-compose logs`
- **No leader elected**: Wait a few seconds for election to complete
- **Metrics not showing**: Verify Prometheus can reach nodes at `http://localhost:9090/targets`
