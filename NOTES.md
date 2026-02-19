# Steel Browser Orchestrator — Design Notes

## Assumptions

Several design questions were sent to the challenge authors without response. The following assumptions were made to proceed:

- **Session crash recovery** — No attempt is made to recover a session whose worker crashes. The session is lost and the worker slot is freed. 
- **Static worker pool** — Workers are spawned at startup with a fixed count (`--workers`, default 5) rather than auto-scaling based on load. Dynamic scaling adds complexity (scale-down requires draining sessions) and the challenge doesn't specify a target concurrency level.
- **Worker restart on crash** — Crashed workers are restarted automatically after a 1-second backoff. Accepting failure permanently would shrink the pool over time, eventually starving all requests.
- **Request timeout handling** — Hung requests are cut off after 5 seconds and returned as `502 Bad Gateway`. The worker is not killed immediately; the background health checker detects unresponsive workers and recycles them on its next tick.
---

## Summary

The Steel Browser Orchestrator is a Go-based service designed to manage multiple `steel-browser` worker processes. It provides a unified API for session management, handles request queuing for resource-limited environments, and implements automated failure recovery and session lifecycle management.

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
           │ :3001  │    │ :3002  │    │ :3003  │
           └────────┘    └────────┘    └────────┘
           Isolated steel-browser processes (one session each)
```

### Key Design Principles

1.  **Durable Request Queuing:** Employs a semaphore-based queue to park incoming requests when all workers are busy, ensuring zero-CPU blocking.
2.  **Callback-Driven Lifecycle:** Uses an `OnCrash` callback system to ensure immediate session cleanup when a worker process terminates unexpectedly.
3.  **Minimalist Implementation:** Built exclusively with the Go standard library (`net/http`, `os/exec`, `sync`, `channels`) to minimize dependency overhead.

### Worker Startup

Each worker is an isolated `steel-browser` process spawned via `os/exec`. Port assignment is sequential starting from `--base-port` (default `3001`): worker 0 gets `:3001`, worker 1 `:3002`, and so on — the `PORT` environment variable is injected at launch.

Before a worker enters the pool, `waitForReady()` polls `GET /health` every 200ms for up to 6 seconds (30 attempts). Once the worker responds `200 OK`, its state transitions `Starting → Available` and it is pushed into the `available` channel. If it never becomes healthy (e.g. port conflict, slow startup), it is marked `Unhealthy` and stays out of the pool until the background health checker detects and recycles it.

### Configuration

The orchestrator is configured via CLI flags at startup:

| Flag | Default | Description |
| :--- | :--- | :--- |
| `--workers` | `5` | Number of worker processes to spawn |
| `--port` | `8080` | Orchestrator listen port |
| `--binary` | `./steel-browser` | Path to the `steel-browser` binary |
| `--base-port` | `3001` | Starting port for worker processes |

```bash 
 ./steel-orchestrator -workers=10 -binary=./steel-browser -port=8080 -base-port=3001
```

---

## Request Queuing

To handle high concurrency with limited worker resources, the orchestrator implements a blocking queue. Instead of rejecting requests with a `503 Service Unavailable` error, the orchestrator parks the request goroutine until a worker is released.



### Implementation Detail

The worker pool maintains an `available` channel (buffered to the worker count).

*   **Acquisition:** Requests attempt to read from the channel. If empty, the goroutine parks effectively, waiting for the Go scheduler to wake it once a worker is returned.
*   **Release:** Workers are returned to the channel in three scenarios:
    *   Explicit session deletion by the client.
    *   Automatic TTL expiration due to inactivity.
    *   Process restart following a crash.

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
---

## Failure Handling

The system is designed to tolerate the inherent instability of browser workers. 

| Failure Mode | Detection Mechanism | Recovery Strategy |
| :--- | :--- | :--- |
| **Process Crash** | `cmd.Wait()` in monitor goroutine | Restarts process after 1s; cleans up stale mapping via `OnCrash` |
| **Request Hang** | `http.Client` timeout (5s) | Returns `502 Bad Gateway`; allows health checker to recycle worker |
| **Worker Unresponsive** | Periodic `/health` check (5s) | Forcibly terminates process, triggering the monitor's restart loop |


### Retry on Forward Failure

Workers can die between being acquired and receiving a request (EOF, connection reset). The orchestrator handles this per-endpoint:

- **POST /sessions** — retries with a new worker (up to 2 attempts). The dead worker is killed so the monitor goroutine restarts it. Since no session exists yet, any healthy worker can serve the request.
- **GET /sessions/:id** — if the forward fails, the session is lost with the dead worker. Cleans up the stale mapping and returns 404.
- **DELETE /sessions/:id** — mapping is removed first, returns 204 even if the forward fails.

---

## Session Lifecycle (TTL)

Sessions are subject to a 60-second inactivity timeout to ensure worker slots are freed for new requests.

1.  **Activity Tracking:** Every successful `GET` request refreshes a `LastAccessed` timestamp.
2.  **Background Reaper:** A dedicated goroutine sweeps the session manager every 5 seconds.
3.  **Cleanup Path:** Expired sessions are deleted from the worker, the mapping is removed, and the worker is released back to the global pool.

---

## Tester

The Rust test suite (`tester/`) covers four test groups:

| Group | Test | What it verifies |
| :--- | :--- | :--- |
| **CRUD** | Create session | POST returns valid `id`, `created_at`, and `data` |
| | Get session | GET returns the same session that was created |
| | Delete session | DELETE returns 204; subsequent GET returns 404 |
| | 404 on missing | GET with an unknown ID returns 404 |
| **Concurrency** | 10 parallel creates | All 10 simultaneous POST requests succeed and return unique IDs |
| **TTL** | Session TTL (60s) | Waits 67s (TTL + sweep interval); verifies GET returns 404 |
| **Recovery** | Worker failure recovery | Creates sessions, deletes all, re-creates — verifies pool is still functional |

> **Note:** The recovery test validates pool recycling behavior, not process-crash recovery specifically. Deterministic crash testing requires a mock binary with configurable failure modes — listed as a future improvement.

---

## Production Gaps

1. **No persistence** — session mappings are in-memory. Orchestrator restart = all sessions lost.
2. **Single orchestrator** — no horizontal scaling. Single point of failure.
3. **No rate limiting** — unbounded queue depth could cause memory issues under extreme load.
4. **No metrics/observability** 
5. **Health check interval** — 5s is coarse. A worker could be dead for up to 5s before detection.
6. **No graceful orchestrator shutdown** — on shutdown, in-flight requests are killed immediately.

## Improvements

1. **Bounded queue with backpressure** — cap the waiting queue at N requests, reject beyond that with 503 + `Retry-After` header.
2. **Dynamic pool sizing** — scale workers up/down based on queue depth.
3. **Circuit breaker** per worker — stop routing to a worker that's failing repeatedly.
4. **Mock worker binary for deterministic testing** — a lightweight Rust binary implementing the `steel-browser` API with configurable failure modes (`crash_after=5s`, `hang_on_create`, `slow_health`). The tester would spawn the orchestrator pointed at this mock binary, enabling reproducible tests for every failure scenario (crash recovery, timeout handling, health check detection) without depending on the real binary's random instability. This would make CI tests reliable and exhaustive.

## Scaling

For multi-region or horizontal scaling:

1. **Shared session registry** (Redis/etcd) — maps `session_id → region:orchestrator:worker` so any orchestrator can route any session.
2. **Consistent hashing** on session IDs distributes load across orchestrator instances behind a load balancer.
3. **Each orchestrator manages its local pool** of workers — no cross-region process management.
4. **Session affinity** at the LB layer — once a session is created, subsequent requests go to the same orchestrator (avoids registry lookups on the hot path).

## Trade-offs

**Blocking queue over fast-fail rejections**
Requests park and wait when all workers are busy rather than getting an immediate `503`. This absorbs burst traffic at the cost of latency predictability — clients can't tell if they're queued or stuck, and an unbounded queue under sustained load could exhaust memory. A bounded queue with a `Retry-After` header would be more honest about capacity.

**Session affinity over fault tolerance**
Sessions are hard-pinned to one worker, which keeps routing simple with no distributed state. The downside is that a worker crash always means session loss — there's no migration path. That's acceptable here since `steel-browser` holds all session state internally with no externalization API.

**Stdlib-only over observability**
Zero external dependencies keeps the binary self-contained, but sacrifices structured logging, metrics, and tracing. Without an observability layer, diagnosing why a worker is cycling or why latency spiked is pure log-grep archaeology.

**Eager restart over conservative slot management**
Crashed workers restart after a 1-second delay to avoid tight loops. During that window the slot is technically acquired but unusable. Keeping the slot out of the pool until the worker is healthy would be safer, but would shrink the effective pool size during recovery bursts.
