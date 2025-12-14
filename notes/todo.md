High priority — core functionality
✅ Dynamic cluster membership [DONE]
✅ Add/remove nodes at runtime (Raft membership changes)
✅ AddNode() / RemoveNode() RPCs, configuration change log entries
✅ Leader transfer [DONE]
✅ Graceful leader handoff
✅ TransferLeadership() for maintenance
✅ Follower reads [DONE]
Allow reads from followers with stale data
Lease-based reads (300ms validity after heartbeat)
CLI flag: --allow-stale for get command
Client-side improvements
Automatic leader discovery/redirect
Connection pooling and retries
Currently: CLI connects to one node, no retries
Needed: Client SDK with retry logic, leader caching
Medium priority — production readiness
Error handling and edge cases
Network partition handling
Split-brain detection
Better timeout/backoff strategies
Currently: basic error handling
Performance optimizations
Batch log replication
Pipeline AppendEntries RPCs
Connection reuse for RPCs (currently creates new connections each time)
Currently: one RPC per peer per heartbeat
Testing
Chaos testing (random node failures)
Network partition tests
Load testing
Currently: basic e2e tests
Documentation
Architecture deep-dive
API documentation
Deployment guides
Currently: Basic README
Lower priority — advanced features
Sharding/partitioning (Phase 5)
Consistent hashing or range partitioning
Multiple Raft groups
Shard rebalancing
Currently: Single Raft group
Web dashboard (Phase 7)
Cluster topology visualization
Real-time metrics
Admin UI
Currently: Grafana dashboards only
REST API
HTTP/JSON endpoint
Currently: gRPC only
Client SDK/library
Go client library
Connection management
Currently: CLI only
Nice to have — future
Transactions
Distributed transactions (2PC)
Multi-key operations
Query layer
Simple SQL-like queries
Indexing
Security
TLS/authentication
Authorization
