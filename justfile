# Steel Browser Orchestrator - Development Commands
# Usage: just <recipe>

# Default: list all recipes
default:
    @just --list

# ─── Orchestrator (Go) ─────────────────────────────────────────

# Build the orchestrator binary
build:
    cd orchestrator && go build -o ../steel-orchestrator .

# Run the orchestrator (default: 3 workers)
run workers="10": build
    ./steel-orchestrator -workers={{workers}} -binary=./steel-browser -port=8080 -base-port=3001

# Run orchestrator with race detector for debugging
run-race workers="10":
    cd orchestrator && go run -race . -workers={{workers}} -binary=../steel-browser -port=8080 -base-port=3001

# Vet and check Go code
check:
    cd orchestrator && go vet ./...

# ─── Tester (Rust) ─────────────────────────────────────────────

# Build the tester
build-tester:
    cd tester && cargo build

# Build the tester in release mode
build-tester-release:
    cd tester && cargo build --release

# Run the tester against the orchestrator
test url="http://localhost:8080":
    cd tester && cargo run -- --url {{url}}

# ─── Benchmarks (k6) ───────────────────────────────────────────

# Run k6 load test
bench:
    k6 run bench/bench.js

# ─── Full Stack ─────────────────────────────────────────────────

# Kill anything on the dev ports (8080, 3001-3005)
kill:
    -kill $(lsof -t -i:8080 -i:3001 -i:3002 -i:3003 -i:3004 -i:3005 -i:3006 -i:3007 -i:3008 -i:3009 -i:3010) 2>/dev/null; true

# Kill, rebuild, and run everything fresh
fresh workers="10": kill build
    sleep 1
    ./steel-orchestrator -workers={{workers}} -binary=./steel-browser -port=8080 -base-port=3001

# ─── Quick Checks ──────────────────────────────────────────────

# Quick health check
health:
    @curl -s http://localhost:8080/health && echo

# Pool status
status:
    @curl -s http://localhost:8080/status | python3 -m json.tool

# Create a test session
create-session data='{"user":"test"}':
    @curl -s -X POST http://localhost:8080/sessions -H "Content-Type: application/json" -d '{{data}}' | python3 -m json.tool

# Get a session by ID
get-session id:
    @curl -s http://localhost:8080/sessions/{{id}} | python3 -m json.tool

# Delete a session by ID
delete-session id:
    @curl -s -X DELETE http://localhost:8080/sessions/{{id}} -w "\nHTTP %{http_code}\n"
