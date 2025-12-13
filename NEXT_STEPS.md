# Next Steps for Mimori

## ✅ Just Completed
- **Dynamic Cluster Membership** - Add/remove nodes at runtime is working!

## 🎯 Recommended Priority Order

### 1. **Testing & Validation** (1-2 days)
**Why first:** Ensure the membership feature is robust before building more.

- [ ] Add unit tests for config changes (add/remove edge cases)
- [ ] Add integration tests with network partitions
- [ ] Test membership changes during concurrent operations
- [ ] Load testing with membership changes

**Files to create:**
- `internal/raft/raft_membership_test.go`
- `tests/membership_test.go`

### 2. **Leader Transfer** (2-3 days) 
**Why:** Critical for zero-downtime maintenance. High priority from TODO.

**What to build:**
- `TransferLeadership()` RPC method
- Leader step-down protocol
- Handoff coordination to target node

**Files to modify:**
- `internal/raft/raft.go` - Add TransferLeadership logic
- `internal/raft/rpc.go` - Add TransferLeadership RPC handler
- `proto/raft.proto` - Add TransferLeadership message
- `cmd/mimorictl/main.go` - Add `transfer-leadership` command

**Reference:** Raft paper section on leadership transfer

### 3. **Follower Reads** (2-3 days)
**Why:** Reduces load on leader, improves read scalability.

**What to build:**
- Read-only mode in KV server
- Lease-based read safety (leader heartbeat lease)
- Client option to allow stale reads

**Files to modify:**
- `internal/api/server.go` - Add follower read support
- `proto/kv.proto` - Add read consistency option
- `cmd/mimorictl/main.go` - Add `--stale` flag for reads

### 4. **Client SDK Improvements** (3-4 days)
**Why:** Better UX and production readiness.

**What to build:**
- Automatic leader discovery and redirect
- Connection pooling and reuse
- Retry logic with exponential backoff
- Client library in `internal/client/` or separate repo

**Files to create:**
- `internal/client/client.go` - Go client library
- `internal/client/pool.go` - Connection pooling
- `internal/client/retry.go` - Retry logic

### 5. **Performance Optimizations** (3-5 days)
**Why:** Better throughput and latency.

**What to build:**
- Batch log replication (send multiple entries per RPC)
- Pipeline AppendEntries (don't wait for one to finish before sending next)
- Connection reuse for RPCs (currently creates new connections)
- Batch client operations

**Files to modify:**
- `internal/raft/rpc_client.go` - Connection pooling, batching
- `internal/raft/raft.go` - Batch entry replication

### 6. **Documentation** (1-2 days)
**Why:** Critical for adoption and maintenance.

**What to write:**
- Architecture deep-dive (how Raft works in Mimori)
- API documentation (all endpoints)
- Deployment guides (production best practices)
- Troubleshooting guide
- Membership change procedures

**Files to create:**
- `docs/architecture.md`
- `docs/api.md`
- `docs/deployment.md`
- `docs/troubleshooting.md`

## 🚧 Medium Priority

### Error Handling & Edge Cases
- Network partition handling improvements
- Split-brain detection
- Better timeout/backoff strategies
- Graceful degradation

### Chaos Testing
- Random node failures during operations
- Network partition simulations
- Leader failure during membership changes
- Disk failures and recovery

## 🎨 Lower Priority (Future)

### Sharding/Partitioning
- Consistent hashing or range partitioning
- Multiple Raft groups
- Shard rebalancing

### Web Dashboard
- Cluster topology visualization
- Real-time metrics
- Admin UI for membership changes

### REST API
- HTTP/JSON endpoints
- OpenAPI/Swagger docs

### Security
- TLS/authentication
- Authorization (RBAC)

## 💡 Quick Wins (Pick Any)

If you want something quick before tackling bigger features:

1. **Better logging** - Add structured context to all logs
2. **Health check improvements** - More granular health states
3. **Metrics expansion** - Add membership change metrics
4. **CLI improvements** - Better error messages, progress indicators
5. **Docker improvements** - Multi-stage builds, healthchecks

## 🎯 My Recommendation

**Start with Testing (Option 1)** because:
- Validates what you just built
- Prevents regressions as you add features
- Gives confidence for production use

**Then Leader Transfer (Option 2)** because:
- High value for operations
- Relatively isolated feature
- Completes the "maintenance" story

What would you like to tackle next?

