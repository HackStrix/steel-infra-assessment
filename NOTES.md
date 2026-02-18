# Steel Browser Orchestrator — Design Notes

## Summary

A Go orchestrator that manages multiple `steel-browser` worker processes behind a single API. Each worker holds exactly one browser session — the orchestrator handles routing, pooling, failure recovery, and session TTL.

**Stack:** Go (stdlib only, zero dependencies) for the orchestrator, Rust for the tester.

### Architecture

```
                     ┌──────────────────────────────┐
                     │       Orchestrator (:8080)    │
                     │                              │
  Client ──HTTP──▶  │  ┌──────────┐  ┌──────────┐  │
                     │  │ Session  │  │  Worker   │  │
                     │  │ Manager  │  │   Pool    │  │
                     │  └────┬─────┘  └────┬──────┘  │
                     │       │             │         │
                     └───────┼─────────────┼─────────┘
                             │             │
               ┌─────────────┼─────────────┼──────────────┐
               │             │             │              │
          ┌────▼───┐    ┌────▼───┐    ┌────▼───┐
          │ Worker │    │ Worker │    │ Worker │
          │ :3001  │    │ :3002  │    │ :3003  │
          └────────┘    └────────┘    └────────┘
          steel-browser processes (one session each)
```

### Key Design Decisions

1. **Channel-based request queuing** instead of immediate 503 rejection
2. **Callback-driven crash cleanup** instead of polling for stale sessions
3. **Stdlib only** — no external dependencies, just `net/http`, `os/exec`, `sync`, channels
4. **Process-per-worker** — each worker is a separate OS process on its own port

---

## Request Queuing (Core Design)

When all workers are busy, incoming requests **block and wait** rather than getting rejected. This is implemented using a Go buffered channel as a semaphore.

### How It Works

```
available channel (capacity = worker count):

STARTUP:     [W0, W1, W2]     ← all workers pushed in after health check passes

REQUEST 1:   [W1, W2]         ← W0 popped instantly (non-blocking)
REQUEST 2:   [W2]             ← W1 popped instantly
REQUEST 3:   []               ← W2 popped instantly

REQUEST 4:   [] ← BLOCKS      goroutine parks here, zero CPU, waiting...

  ...client deletes session on W0...

             [W0]             ← W0 pushed back via Release()
REQUEST 4:   []               ← W0 popped, goroutine wakes up, request proceeds
```

### The Acquire/Release Pattern

```go
// Acquire — blocks until a worker is free (or 30s timeout)
func (p *Pool) Acquire(ctx context.Context) (*Worker, error) {
    select {
    case w := <-p.available:   // worker freed up
        return w, nil
    case <-ctx.Done():         // 30s timeout
        return nil, err
    }
}

// Release — returns worker to the pool (non-blocking)
func (p *Pool) Release(w *Worker) {
    p.available <- w
}
```

### Three Ways a Worker Returns to the Pool

| Trigger | Code Path | When |
|---------|-----------|------|
| Session deleted | `DELETE /sessions/:id` → `SetSessionID("")` → `Release()` | Client explicitly frees it |
| TTL expires | Background ticker every 5s → `expireStale()` → `SetSessionID("")` → `Release()` | 60s inactivity |
| Worker restarts | `monitor()` detects crash → restart → `waitForReady()` → `Release()` | Process dies and comes back |

### Why Channels, Not Polling

The previous approach iterated over workers checking `state == available` — if none free, immediate 503. The channel approach is **zero-CPU blocking**: Go's scheduler parks the goroutine and wakes it the instant a worker is released. No spin-waiting, no polling interval.

---

## Failure Handling

Workers are intentionally unstable. Three failure modes and how we handle them:

| Failure | Detection | Recovery |
|---------|-----------|----------|
| **Process crash** | `cmd.Wait()` returns in monitor goroutine | Auto-restart after 1s, `OnCrash` callback cleans stale session |
| **Request hang** | `http.Client` with 5s timeout → `context.DeadlineExceeded` | Return 502 to client, health check will catch and kill |
| **Unresponsive** | Periodic `GET /health` fails (every 5s) | Kill process → monitor goroutine handles restart |

### Crash → Session Cleanup Flow

```
Worker 1 crashes (exit status 1)
  → monitor() goroutine wakes up
  → OnCrash("session-abc") callback fires
  → SessionManager.Remove("session-abc")     ← stale mapping cleaned
  → Worker restarts after 1s
  → waitForReady() polls /health
  → pool.Release(worker1)                     ← back in the queue
  → blocked request 4 wakes up               ← served by restarted worker
```

---

## Session TTL

Sessions expire after 60 seconds of inactivity. "Inactivity" = no `GET /sessions/:id` calls.

- Every `GET` updates `LastAccessed` timestamp
- Background goroutine sweeps every 5s
- Expired sessions are `DELETE`d from the worker and removed from the mapping
- Worker is released back to the available pool

---

## Production Gaps

1. **No persistence** — session mappings are in-memory. Orchestrator restart = all sessions lost.
2. **Single orchestrator** — no horizontal scaling. Single point of failure.
3. **No rate limiting** — unbounded queue depth could cause memory issues under extreme load.
4. **No metrics/observability** — no Prometheus metrics, no structured logging, no tracing.
5. **Health check interval** — 5s is coarse. A worker could be dead for up to 5s before detection.
6. **No graceful drain** — on shutdown, in-flight requests are killed immediately.

## Improvements

1. **Bounded queue with backpressure** — cap the waiting queue at N requests, reject beyond that with 503 + `Retry-After` header.
2. **Structured logging** (e.g., `slog` or `zerolog`) with request IDs for traceability.
3. **Prometheus metrics** — pool utilization, queue depth, request latency histograms, crash counts.
4. **Graceful shutdown** — drain in-flight requests before stopping workers.
5. **Dynamic pool sizing** — scale workers up/down based on queue depth.
6. **Circuit breaker** per worker — stop routing to a worker that's failing repeatedly.

## Scaling

For multi-region or horizontal scaling:

1. **Shared session registry** (Redis/etcd) — maps `session_id → region:orchestrator:worker` so any orchestrator can route any session.
2. **Consistent hashing** on session IDs distributes load across orchestrator instances behind a load balancer.
3. **Each orchestrator manages its local pool** of workers — no cross-region process management.
4. **Session affinity** at the LB layer — once a session is created, subsequent requests go to the same orchestrator (avoids registry lookups on the hot path).

## Trade-offs

| Optimized For | Sacrificed |
|--------------|-----------|
| **Simplicity** — stdlib only, ~400 lines total | No external dependency benefits (metrics, structured logging) |
| **Correctness** — every session routed to exactly one worker | No session migration on crash (session is lost) |
| **Resilience** — auto-restart, crash cleanup, TTL | Restart latency (~1s) means brief unavailability of that slot |
| **Zero-CPU queuing** — channel-based blocking | Unbounded queue depth (no backpressure) |
