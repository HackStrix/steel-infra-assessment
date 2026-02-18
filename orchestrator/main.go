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
	workers := flag.Int("workers", 5, "number of worker processes to spawn")
	port := flag.Int("port", 8080, "orchestrator listen port")
	binary := flag.String("binary", "./steel-browser", "path to the steel-browser binary")
	basePort := flag.Int("base-port", 3001, "starting port for worker processes")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("Starting orchestrator: workers=%d, port=%d, binary=%s", *workers, *port, *binary)

	// Create pool
	pool, err := NewPool(*workers, *binary, *basePort)
	if err != nil {
		log.Fatalf("Failed to create worker pool: %v", err)
	}
	
	// create session manager
	sessions, err := NewSessionManager()
	if err != nil {
		log.Fatalf("Failed to create session manager: %v", err)
	}

	// setting up callback for worker crashes
	for _, w := range pool.Workers() {
		w := w // capture loop variable
		// passed through dep injection to decouple pool and sessionManager
		w.OnCrash = func(sessionID string) {
			log.Printf("[main] cleaning up stale session %s after worker %d crash", sessionID, w.ID)
			sessions.Remove(sessionID)
		}
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

// handleCreateSession handles POST /sessions
func handleCreateSession(w http.ResponseWriter, r *http.Request, pool *Pool, sessions *SessionManager) {
	// Read request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	// Wait for an available worker (blocks until one is free, up to 30s)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	worker, err := pool.Acquire(ctx)
	if err != nil {
		http.Error(w, "no workers available (queue timeout)", http.StatusServiceUnavailable)
		return
	}

	// Forward to the worker
	respBody, statusCode, err := forwardCreateSession(worker, body)
	if err != nil {
		http.Error(w, fmt.Sprintf("worker error: %v", err), http.StatusBadGateway)
		return
	}

	// Parse response to extract session ID
	var sessionResp struct {
		ID        string          `json:"id"`
		CreatedAt json.RawMessage `json:"created_at"`
		Data      json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(respBody, &sessionResp); err != nil {
		http.Error(w, "failed to parse worker response", http.StatusInternalServerError)
		return
	}

	// Register the session mapping
	sessions.Add(sessionResp.ID, worker)
	worker.SetSessionID(sessionResp.ID)

	// Return the response to the client
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	w.Write(respBody)
}

// handleGetSession handles GET /sessions/:id
func handleGetSession(w http.ResponseWriter, r *http.Request, sessions *SessionManager, sessionID string) {
	// Look up which worker holds this session
	worker := sessions.Get(sessionID)
	if worker == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	// Forward to the worker
	respBody, statusCode, err := forwardGetSession(worker, sessionID)
	if err != nil {
		http.Error(w, fmt.Sprintf("worker error: %v", err), http.StatusBadGateway)
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
		"active_sessions": sessions.Count(),
		"workers":         workerStatus,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}
