# Steel Browser Orchestrator Challenge

Build an orchestration layer that manages multiple browser session workers. This challenge helps us evaluate your ability to work with distributed systems, handle state management, and build resilient infrastructure. Choose any programming language you're comfortable with to implement your solution.

## Background

We have a service called `steel-browser` — a simplified version of our production browser infrastructure. It's a stateful binary that holds exactly **one browser session at a time**. If you create a new session, the previous one is gone.

In production, we need to handle thousands of concurrent browser sessions. Your job is to build an orchestrator that manages multiple `steel-browser` instances to support this scale.

We're giving you the compiled binary (details on this at the bottom). Figure out how it works and build around it.

## Requirements

1. Build an orchestrator that wraps multiple `steel-browser` instances
2. Build a tester (in a different language) that verifies your orchestrator works
3. Document your decisions in `NOTES.md`

### Worker Binary API

The `steel-browser` binary exposes this API:

```
GET  /status              → { "available": bool, "session_id": string|null }
POST /sessions            → { "id", "created_at", "data" }
GET  /sessions/:id        → { "id", "created_at", "data" } | 404
DELETE /sessions/:id      → 204 | 404
GET  /health              → "ok"

```

**Warning**: Workers are not perfectly stable. They may crash, hang, or become unresponsive. Your orchestrator should handle this gracefully.

---

## Part 1: Session Orchestrator

### Core Functionality

Your orchestrator should expose the same API shape and handle routing, pooling, and failures:

```bash
# Create a session (routed to an available worker)
curl -X POST <http://localhost:8080/sessions> \\
  -H "Content-Type: application/json" \\
  -d '{"user": "test"}'

# Get session (routed to the correct worker)
curl <http://localhost:8080/sessions/><session-id>

# Delete session
curl -X DELETE <http://localhost:8080/sessions/><session-id>

```

### Requirements

- [ ]  Expose `POST`/`GET`/`DELETE` `/sessions` endpoints
- [ ]  Route requests to the correct worker based on session ID
- [ ]  Manage a pool of workers (spawn, monitor, restart)
- [ ]  Handle worker failures gracefully (crashes, hangs, unresponsive)
- [ ]  **Implement session TTL**: Sessions expire after **60 seconds** of inactivity and return 404

### Expected Behavior

```bash
$ curl -X POST <http://localhost:8080/sessions> -d '{"user": "alice"}'
{"id": "abc123", "created_at": "2024-01-15T10:30:00Z", "data": {"user": "alice"}}

$ curl <http://localhost:8080/sessions/abc123>
{"id": "abc123", "created_at": "2024-01-15T10:30:00Z", "data": {"user": "alice"}}

# After 60 seconds of inactivity...
$ curl <http://localhost:8080/sessions/abc123>
404 Not Found

# Non-existent session
$ curl <http://localhost:8080/sessions/invalid>
404 Not Found

```

---

## Part 2: Orchestrator Tester

Build a tool that verifies your orchestrator works correctly.

**Important**: Use a different language than Part 1. If you wrote a Go orchestrator, write a Rust tester. If you wrote Rust, write Go.

### Requirements

- [ ]  Verify basic CRUD operations work
- [ ]  Test concurrent session handling
- [ ]  Test session TTL expiration
- [ ]  Report results clearly

### Expected Output

```bash
$ ./tester --url <http://localhost:8080>

━━━━━━━━━━━━━━━━━━━━━━━━━━━━
🧪 ORCHESTRATOR TEST SUITE
━━━━━━━━━━━━━━━━━━━━━━━━━━━━

✓ Create session
✓ Get session
✓ Delete session
✓ 404 on missing session
✓ Concurrent sessions (10 parallel)
✓ Session TTL expiration (60s)
✓ Worker failure recovery

━━━━━━━━━━━━━━━━━━━━━━━━━━━━
📊 RESULTS: 7/7 passed
━━━━━━━━━━━━━━━━━━━━━━━━━━━━

```

---

## [NOTES.md](http://notes.md/) (Required)

Along with your code, include a `NOTES.md` that covers:

1. **Summary**: What you built, how it works, key design decisions
2. **Production gaps**: What might break under load? What's missing for production?
3. **Improvements**: If you had more time, what would you change?
4. **Scaling**: How would you handle workers distributed across multiple regions?
5. **Trade-offs**: What did you optimize for and what did you sacrifice?

**We read the notes before we read the code. Don't skip this.**

---

## What We're Looking For

- Can you figure out an undocumented system?
- How do you handle state and routing?
- Do you understand failure modes?
- Can you explain your decisions clearly?

Rough edges are fine if you explain them. Don't spend time on unit tests — we'll test it ourselves.

---

## Getting Started

Download the worker binary:

```bash
# Linux (amd64)
curl -L -o steel-browser https://steel-challenge-releases.s3.us-east-2.amazonaws.com/steel-browser-linux-amd64
chmod +x steel-browser

# Linux (arm64)
curl -L -o steel-browser https://steel-challenge-releases.s3.us-east-2.amazonaws.com/steel-browser-linux-arm64
chmod +x steel-browser

# macOS (arm64 / Apple Silicon)
curl -L -o steel-browser https://steel-challenge-releases.s3.us-east-2.amazonaws.com/steel-browser-darwin-arm64
chmod +x steel-browser

# Windows (amd64)
curl -L -o steel-browser.exe https://steel-challenge-releases.s3.us-east-2.amazonaws.com/steel-browser-windows-amd64.exe

```

Run it:

```bash
PORT=3001 ./steel-browser

```

Test it:

```bash
# Check status (should be available)
curl http://localhost:3001/status

# Create a session
curl -X POST http://localhost:3001/sessions \\
  -H "Content-Type: application/json" \\
  -d '{"user": "test"}'

# Check status again (should show session_id)
curl http://localhost:3001/status

```

---

## Submission Guidelines

When you're finished, submit your code by replying to your application email with either:

- A GitHub repository link with your solution
- A GitHub Gist link with your code
- Your code inline in the email

### Project Structure

```
/orchestrator     ← your orchestrator code
/tester           ← your tester code (different language)
NOTES.md          ← your design notes

```

<aside>
⛔

***Note:** If you were invited to this challenge via a no-reply email, please share your solution via email to [hussien@steelbrowser.com](mailto:hussien@steelbrowser.com).*

</aside>

---

## Questions?

Ask us! We want to see you at your best. You can only gain points by asking questions, never lose them.