# Follower Reads Explained

## What Are Follower Reads?

Follower reads allow clients to read data from **follower nodes** (non-leader nodes) instead of always going to the leader. This improves scalability and reduces load on the leader.

## Why Do We Need Follower Reads?

In a Raft-based distributed database like MimoriDB, there are typically multiple nodes:

- **1 Leader**: Handles all writes and traditionally all reads
- **N Followers**: Receive replicated data from the leader but normally cannot serve reads

Without follower reads, if you have 5 nodes but only 1 leader, all 100% of read requests go to that single leader node, creating a bottleneck.

With follower reads, reads can be distributed across all nodes, improving:

- **Scalability**: Read throughput scales with cluster size
- **Latency**: Clients can read from geographically closer nodes
- **Leader Load**: Reduces pressure on the leader

## How Do Follower Reads Work?

### The Problem: Stale Data

The main challenge with follower reads is **stale data**. Followers receive updates from the leader asynchronously, so they might not have the latest data immediately.

Consider this scenario:

1. Client writes "value=v2" to leader
2. Leader commits the write (index 100)
3. Leader sends AppendEntries to follower (in flight)
4. Client reads from follower → might get old value "v1" if replication hasn't completed yet

### The Solution: Read Lease

To prevent serving dangerously stale data, we use a **read lease** mechanism:

```
Follower has read lease if:
  - Follower has received a heartbeat from leader within last 300ms
  - This means follower is "in contact" with the active cluster
```

The logic:

1. Leader sends heartbeats every ~75ms to followers
2. Each heartbeat updates a timestamp on the follower
3. Follower checks: "Is my last heartbeat < 300ms ago?"
4. If yes → Follower can serve reads (has valid lease)
5. If no → Follower rejects reads (might be partitioned from cluster)

### Why 300ms?

- Election timeout is ~150-300ms (randomized)
- If a follower hasn't received a heartbeat in 300ms, the leader might be dead
- A new leader might have been elected → follower's data could be stale or wrong
- Rejecting reads prevents serving data from a partitioned node

## Consistency Guarantees

### Strong Consistency (Default)

```bash
mimorictl get key
```

- Reads always go to the leader
- Guarantees linearizability (strongest consistency)
- Always returns the latest committed value

### Stale Consistency (Follower Reads)

```bash
mimorictl get key --allow-stale
```

- Can read from followers if they have a valid lease
- May return data that is at most ~300ms stale
- Better performance but weaker consistency guarantee

## Example Scenario

Imagine a 3-node cluster:

- Node A (Leader): Handles writes
- Node B (Follower): Replicates data
- Node C (Follower): Replicates data

**Without Follower Reads:**

```
Client 1 → Node A (read) ✓
Client 2 → Node A (read) ✓
Client 3 → Node A (read) ✓
Client 4 → Node A (read) ✓
```

All reads hit the same leader.

**With Follower Reads:**

```
Client 1 → Node A (read) ✓ (leader)
Client 2 → Node B (read --allow-stale) ✓ (follower)
Client 3 → Node C (read --allow-stale) ✓ (follower)
Client 4 → Node B (read --allow-stale) ✓ (follower)
```

Reads are distributed across all nodes.

## Safety Properties

1. **Write Safety**: All writes still go through leader (unchanged)
2. **Read Safety**: Followers only serve reads if they have a valid lease (recent heartbeat)
3. **Partition Detection**: If follower loses contact with leader, it stops serving reads
4. **Backward Compatible**: Default behavior unchanged (leader-only reads)

## Trade-offs

| Aspect          | Leader Reads                      | Follower Reads            |
| --------------- | --------------------------------- | ------------------------- |
| **Consistency** | Strong (linearizable)             | Stale (~300ms)            |
| **Latency**     | May be higher (network to leader) | May be lower (local read) |
| **Throughput**  | Limited by leader                 | Scales with cluster       |
| **Safety**      | Always latest                     | May be stale              |
| **Use Case**    | Critical reads                    | High-volume reads         |

## When to Use Follower Reads?

**Use follower reads when:**

- Reading data that doesn't need to be absolutely up-to-date
- High read throughput is needed
- Reading cached/computed values
- Analytics/aggregation queries

**Use leader reads when:**

- Reading critical data that must be latest
- Financial transactions
- Leader election status
- Any write-before-read scenario

## Implementation Details

### Read Lease Check

```go
func (r *Raft) HasReadLease() bool {
    if r.state == Leader {
        return true  // Leaders always have lease
    }

    if r.state == Follower && r.leader != "" {
        age := time.Since(r.electionReset)  // Last heartbeat time
        return age < 300*time.Millisecond   // Valid if < 300ms
    }

    return false  // No lease
}
```

### API Handler

```go
func (s *Server) Get(ctx context.Context, req *kv.GetRequest) {
    if s.raft.IsLeader() {
        // Leader can always serve reads
        return s.store.Get(req.Key)
    }

    if req.AllowStale && s.raft.HasReadLease() {
        // Follower can serve reads if lease valid
        return s.store.Get(req.Key)
    }

    // Reject: not leader and no valid lease
    return error("not leader or lease expired")
}
```

## Testing

See `test-follower-reads.sh` for a complete test demonstration.

Key test scenarios:

1. Follower reads rejected without `--allow-stale`
2. Follower reads succeed with `--allow-stale` when lease is valid
3. Leader reads always work
4. Lease expiration during leader partition
