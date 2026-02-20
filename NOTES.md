# Steel Browser Orchestrator — Design Notes

> *Written with a little help from Claude Sonnet 4.6. The thinking, decisions, and trade-offs are mine — it just helped me write them down*

## Assumptions

Several design questions were sent to the challenge authors without response. The following assumptions were made to proceed:

- **Session crash recovery** — No attempt is made to recover a session whose worker crashes. The session is lost and the worker slot is freed.
- **Worker restart on crash** — Crashed workers are restarted automatically after a 1-second backoff. Accepting failure permanently would shrink the pool over time, eventually starving all requests.
- **Request timeout handling** — Hung requests are cut off after 5 seconds and returned as `502 Bad Gateway`. The worker is not killed immediately; the background health checker detects unresponsive workers and recycles them on its next tick.
- **Behavior when all workers are busy** — The challenge does not specify what to do when the pool is fully occupied. Rather than immediately rejecting with `503`, the orchestrator triggers a scale-up and blocks the request for up to **5 minutes** waiting for a worker. If none becomes available, it returns `503 Service Unavailable`.

---

## Summary

The Steel Browser Orchestrator is a Go-based service that manages a dynamic pool of `steel-browser` worker processes. It provides a unified API for session management, auto-scales the worker pool in response to demand, and implements automated failure recovery and session lifecycle management.

**Stack:** Go (standard library only) for the orchestrator; Rust for the integration test suite.

### Architecture

```
                     ┌───────────────────────────────┐
                     │       Orchestrator (:8080)    │
                     │                               │
  Client ──HTTP──▶   │  ┌──────────┐  ┌───────────┐  │
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
           │ :OS    │    │ :OS    │    │ :OS    │
           └────────┘    └────────┘    └────────┘
           Isolated steel-browser processes (one session each)
           Ports are assigned by the OS at spawn time
```

### Key Design Principles

1. **Auto-scaling pool** — the worker count floats between `--min-workers` and `--max-workers` based on real demand, rather than being fixed at startup.
2. **Durable request queuing** — uses a semaphore-based channel to park incoming requests when all workers are busy, ensuring zero-CPU blocking while a new worker starts.
3. **Callback-driven lifecycle** — an `OnCrash` callback wired into every worker (including dynamically spawned ones) ensures immediate session cleanup on unexpected process exit.
4. **Minimalist implementation** — built exclusively with the Go standard library (`net/http`, `os/exec`, `sync`, channels) to minimise dependency overhead.

### Worker Startup

Each worker is an isolated `steel-browser` process spawned via `os/exec`. Rather than managing a fixed port range, each worker requests a free port from the OS at spawn time by binding a temporary listener to `127.0.0.1:0`, reading the assigned port, closing the listener, and passing the port to the worker via the `PORT` environment variable. This eliminates all port-range configuration and reclamation bookkeeping.

Before a worker enters the pool, `waitForReady()` polls `GET /health` every 200 ms for up to 6 seconds (30 attempts). Once the worker responds `200 OK`, its state transitions `Starting → Available` and it is pushed into the `available` channel. If it never becomes healthy (slow startup, immediate crash), it is marked `Unhealthy` and stays out of the pool until the background health checker recycles it.

### Configuration

| Flag | Default | Description |
| :--- | :--- | :--- |
| `--min-workers` | `2` | Workers spawned at startup; floor for scale-down |
| `--max-workers` | `10` | Ceiling for scale-up |
| `--port` | `8080` | Orchestrator listen port |
| `--binary` | `./steel-browser` | Path to the `steel-browser` binary |

```bash
./steel-orchestrator -min-workers=2 -max-workers=10 -binary=./steel-browser -port=8080
```

---

## Worker Pool & Auto-Scaling

The pool manages a dynamic set of workers and exposes a semaphore channel (`available`) that acts as both the request queue and the scaling signal.

### Scale-up

When `Acquire()` is called and `available` is empty, the pool checks `len(workers) + pendingAdds < max`. If there is room, it fires `go addWorker()` *before* blocking — so a new worker starts immediately while the caller waits. The `pendingAdds` counter is incremented under the lock before it is released, acting as a slot reservation. This prevents concurrent `Acquire()` calls from each spawning their own worker and overshooting `max`:

```
10 concurrent requests, workers=18, max=20, pendingAdds=0:

goroutine A: lock → 18+0 < 20 ✓ → pendingAdds=1 → unlock → [start worker]
goroutine B: lock → 18+1 < 20 ✓ → pendingAdds=2 → unlock → [start worker]
goroutine C: lock → 18+2 >= 20  → unlock → return (no new worker)
...all others: same as C
→ exactly 2 new workers started, total = 20
```

### Scale-down

A background `scaleLoop` goroutine ticks every 10 s. If `len(available) > 0 && len(workers) > min` for **2 consecutive ticks** (20 s of sustained idleness), one idle worker is removed. The anti-thrash counter resets to 0 whenever the pool is fully occupied, so a burst of requests immediately cancels a pending scale-down. Workers are marked `draining` before being killed so their `monitor()` goroutine exits cleanly instead of restarting.

### Worker lifecycle

```
Acquire()  → popped from available → state: Busy
                                          │
                        ┌─────────────────┼──────────────────┐
                        ▼                 ▼                  ▼
                  DELETE /session     TTL expiry         Worker crash
                        │                 │                  │
                  SetSessionID("")  SetSessionID("")    OnCrash() → Remove()
                        │                 │                  │
                        └────────────────▶▼◀─────────────────┘
                                    pool.Release()
                                  → pushed to available
```

A worker slot remains **busy for the entire lifetime of the session** — not just the duration of the HTTP request. The slot is only freed on explicit DELETE, TTL expiry, or worker crash recovery.

---

## Request Queuing

```
available channel (capacity = max-workers):

STARTUP:     [W0, W1]          ← min=2 workers ready

REQUEST 1:   [W1]              ← W0 popped, state=Busy
REQUEST 2:   []                ← W1 popped, state=Busy

REQUEST 3:   [] ← BLOCKS       goroutine parks; addWorker() fires in background
                               W2 starts, passes /health, pushes itself to available
             [W2]
REQUEST 3:   []                ← W2 popped, goroutine wakes, proceeds

  ...sustained idle for 20s...

             [W0, W2]          ← 2 idle above min=1; scaleLoop removes W2
             [W0]              ← back to min
```

---

## Failure Handling

| Failure Mode | Detection | Recovery |
| :--- | :--- | :--- |
| **Process crash** | `cmd.Wait()` in monitor goroutine | Restart after 1 s; `OnCrash` cleans up stale session mapping |
| **Request hang** | `http.Client` timeout (5 s) | Returns `502`; health checker recycles the worker on next tick |
| **Worker unresponsive** | `/health` poll every 5 s | Force-kill; monitor restarts |
| **Scale-up failure** | `findFreePort()` or `Start()` error | `pendingAdds` decremented; slot returned; logged |

### Retry on forward failure

- **POST /sessions** — retries up to 3 times with different workers. The failed worker is killed so the monitor restarts it.
- **GET /sessions/:id** — if the forward fails, the session is lost; stale mapping removed, returns 404.
- **DELETE /sessions/:id** — mapping removed first; returns 204 even if forward fails.

---

## Session Lifecycle (TTL)

Sessions expire after 60 seconds of inactivity:

1. Every successful `GET` refreshes a `LastAccessed` timestamp.
2. A background sweeper goroutine runs every 5 seconds.
3. Expired entries are deleted from the worker, removed from the session map, and the worker is released back to the pool.

---

## Tester

The Rust test suite (`tester/`) covers four groups:

| Group | Test | What it verifies |
| :--- | :--- | :--- |
| **CRUD** | Create session | POST returns valid `id`, `created_at`, and `data` |
| | Get session | GET returns the same session |
| | Delete session | DELETE returns 204; subsequent GET returns 404 |
| | 404 on missing | GET with unknown ID returns 404 |
| **Concurrency** | 10 parallel creates | All 10 simultaneous POSTs succeed with unique IDs |
| **TTL** | Session TTL (60 s) | Waits 67 s; verifies GET returns 404 |
| **Recovery** | Worker failure recovery | Kills live worker via `/debug/crash-worker`, verifies 404 on crashed session, verifies pool recovers |

> **Implementation note:** Crash recovery testing requires killing a specific worker from outside the orchestrator. A `POST /debug/crash-worker?session_id=:id` endpoint was added that locates and kills the worker holding the given session. This directly exercises the `OnCrash` callback → stale session cleanup → slot release → worker restart path end-to-end.

---

## Production Gaps

1. **No persistence** — session mappings are in-memory. Orchestrator restart = all sessions lost.
2. **Single orchestrator** — no horizontal scaling; single point of failure.
3. **Unbounded queue** — no cap on waiting requests; sustained overload could exhaust goroutine memory.
4. **No metrics/observability** — diagnosing latency spikes or worker churn requires log-grepping.
5. **Health check granularity** — 5 s polling means a dead worker can go undetected for up to 5 s.
---

## Improvements

1. **Bounded queue with backpressure** — cap the waiting queue at N requests, reject beyond that with `503 + Retry-After`.
2. **Circuit breaker per worker** — stop routing to a worker that fails repeatedly before the health checker catches it.
3. **Mock worker binary for deterministic testing** — a lightweight binary implementing the `steel-browser` API with configurable failure modes (`crash_after=5s`, `hang_on_create`, `slow_health`). This would make CI tests reliable and exhaustive without depending on the real binary's random instability.
4. **Structured logging and metrics** — replace `log.Printf` with a structured logger and expose a `/metrics` (Prometheus) endpoint to surface `workers_current`, `workers_pending`, `sessions_active`, `acquire_wait_p99`.
5. **Graceful shutdown** — drain in-flight requests before killing workers.

---

## Scaling

For multi-region or horizontal scaling:

1. **Shared session registry** (Redis/etcd) — maps `session_id → region:orchestrator:worker` so any orchestrator can route any session.
2. **Consistent hashing** on session IDs distributes load across orchestrator instances behind a load balancer.
3. **Each orchestrator manages its local pool** of workers — no cross-region process management.
4. **Session affinity** at the LB layer — once a session is created, subsequent requests go to the same orchestrator instance to avoid registry lookups on the hot path.

---

## Trade-offs

**Reactive scale-up over pre-warming**
Workers are spawned on demand when the pool empties, not in anticipation of load. This means the first request in a burst always waits for a worker to start (~200 ms on a healthy host). Pre-warming (e.g. scale up when `available < threshold`) would reduce latency at the cost of over-provisioning idle workers.

**Anti-thrash delay over responsiveness**
Scale-down requires 2 consecutive 10-second ticks of idleness (20 s total) before removing a worker. This prevents oscillation under bursty traffic but means over-provisioned workers linger longer than necessary.

**Blocking queue over fast-fail rejections**
Requests park and wait rather than getting an immediate `503`. This absorbs burst traffic at the cost of latency predictability — clients can't tell if they're queued or stuck. A bounded queue with `Retry-After` would be more honest about capacity limits.

**Session affinity over fault tolerance**
Sessions are hard-pinned to one worker, keeping routing simple with no distributed state. The downside is that a worker crash always means session loss — there is no migration path. That is acceptable here since `steel-browser` holds all session state internally with no externalization API.

