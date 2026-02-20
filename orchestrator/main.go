package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	minWorkers := flag.Int("min-workers", 2, "minimum (starting) number of worker processes")
	maxWorkers := flag.Int("max-workers", 10, "maximum number of worker processes (auto-scaling ceiling)")
	port := flag.Int("port", 8080, "orchestrator listen port")
	binary := flag.String("binary", "./steel-browser", "path to the steel-browser binary")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("Starting orchestrator: min-workers=%d, max-workers=%d, port=%d, binary=%s", *minWorkers, *maxWorkers, *port, *binary)

	// Create pool
	pool, err := NewPool(*minWorkers, *maxWorkers, *binary)
	if err != nil {
		log.Fatalf("Failed to create worker pool: %v", err)
	}

	// create session manager
	sessions, err := NewSessionManager()
	if err != nil {
		log.Fatalf("Failed to create session manager: %v", err)
	}

	// Wire crash handler for both initial and future scaled-up workers.
	// pool.CrashHandler is picked up by addWorker(); apply it to initial workers too.
	pool.CrashHandler = func(sessionID string) {
		log.Printf("[session] removing stale session %s (worker crashed)", sessionID)
		sessions.Remove(sessionID)
	}
	for _, w := range pool.Workers() {
		w.OnCrash = pool.CrashHandler
	}

	// Wire up HTTP handlers
	mux := http.NewServeMux()

	mux.HandleFunc("/sessions/", func(w http.ResponseWriter, r *http.Request) {
		// Extract session ID from path: /sessions/{id}
		sessionID := strings.TrimPrefix(r.URL.Path, "/sessions/")
		if sessionID == "" {
			http.Error(w, "session ID required", http.StatusBadRequest)
			return
		}

		switch r.Method {
		case http.MethodGet:
			handleGetSession(w, r, sessions, sessionID)
		case http.MethodDelete:
			handleDeleteSession(w, r, sessions, sessionID)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/sessions", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			handleCreateSession(w, r, pool, sessions)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		handleStatus(w, pool, sessions)
	})

	// Debug endpoint — kills the worker holding the given session (for testing only)
	mux.HandleFunc("/debug/crash-worker", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		sessionID := r.URL.Query().Get("session_id")
		if sessionID == "" {
			http.Error(w, "session_id required", http.StatusBadRequest)
			return
		}
		worker, ok := pool.FindBySession(sessionID)
		if !ok {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		log.Printf("[debug] killing worker %d holding session %s", worker.ID, sessionID)
		worker.Kill()
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "worker killed")
	})

	// Graceful shutdown on SIGINT/SIGTERM
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		sig := <-sigCh
		log.Printf("Received %s, shutting down...", sig)
		pool.Shutdown()
		os.Exit(0)
	}()

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("Orchestrator listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

const maxCreateRetries = 3

// handleCreateSession handles POST /sessions
// Retries with a new worker if the first one fails (EOF, crash, etc.)
func handleCreateSession(w http.ResponseWriter, r *http.Request, pool *Pool, sessions *SessionManager) {
	// Read request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	var lastErr error
	for attempt := 0; attempt < maxCreateRetries; attempt++ {
		worker, err := pool.Acquire(ctx)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to acquire worker: %v", err), http.StatusServiceUnavailable)
			http.Error(w, "no workers available (queue timeout)", http.StatusServiceUnavailable)
			return
		}

		respBody, statusCode, err := forwardCreateSession(worker, body)
		if err != nil {
			log.Printf("[handler] create attempt %d/%d failed on worker %d: %v", attempt+1, maxCreateRetries, worker.ID, err)
			lastErr = err
			worker.Kill() // force restart — monitor goroutine handles recovery
			continue
		}

		// Parse response to extract session ID
		var sessionResp struct {
			ID        string          `json:"id"`
			CreatedAt json.RawMessage `json:"created_at"`
			Data      json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(respBody, &sessionResp); err != nil {
			log.Printf("[handler] create attempt %d/%d: bad response from worker %d: %s", attempt+1, maxCreateRetries, worker.ID, string(respBody))
			lastErr = fmt.Errorf("failed to parse worker response")
			worker.Kill()
			continue
		}

		// Success — register the session and return
		sessions.Add(sessionResp.ID, worker)
		worker.SetSessionID(sessionResp.ID)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		w.Write(respBody)
		return
	}

	// All retries exhausted
	http.Error(w, fmt.Sprintf("all workers failed: %v", lastErr), http.StatusBadGateway)
}

// handleGetSession handles GET /sessions/:id
// If the worker holding the session is dead, cleans up the stale mapping and returns 404.
func handleGetSession(w http.ResponseWriter, r *http.Request, sessions *SessionManager, sessionID string) {
	worker := sessions.Get(sessionID)
	if worker == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	respBody, statusCode, err := forwardGetSession(worker, sessionID)
	if err != nil {
		// Worker is dead — session is lost. Clean up the stale mapping.
		log.Printf("[handler] GET forward failed, session %s lost (worker %d dead): %v", sessionID, worker.ID, err)
		sessions.Remove(sessionID)
		worker.Kill()
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	w.Write(respBody)
}

// handleDeleteSession handles DELETE /sessions/:id
func handleDeleteSession(w http.ResponseWriter, r *http.Request, sessions *SessionManager, sessionID string) {
	// Look up and remove the session mapping
	worker := sessions.Remove(sessionID)
	if worker == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	// Forward delete to the worker
	statusCode, err := deleteSessionFromWorker(worker, sessionID)
	if err != nil {
		// Session already removed from our mapping; worker might be down
		log.Printf("[handler] DELETE forward failed for session %s: %v", sessionID, err)
		worker.SetSessionID("")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Free the worker
	worker.SetSessionID("")

	w.WriteHeader(statusCode)
}

// handleStatus returns pool and session status for debugging.
func handleStatus(w http.ResponseWriter, pool *Pool, sessions *SessionManager) {
	workers := pool.Workers()
	workerStatus := make([]map[string]interface{}, len(workers))
	for i, wr := range workers {
		workerStatus[i] = map[string]interface{}{
			"id":         wr.ID,
			"port":       wr.Port,
			"state":      wr.State().String(),
			"session_id": wr.SessionID(),
		}
	}

	status := map[string]interface{}{
		"active_sessions":   sessions.Count(),
		"worker_count":      len(workers),
		"available_workers": pool.QueueDepth(),
		"min_workers":       pool.Min(),
		"max_workers":       pool.Max(),
		"workers":           workerStatus,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}
