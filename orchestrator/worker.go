package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"
)

type WorkerState int

const (
	WorkerStateStarting WorkerState = iota
	WorkerStateAvailable
	WorkerStateBusy
	WorkerStateUnhealthy
	WorkerStateDead
)

func (s WorkerState) String() string {
	switch s {
	case WorkerStateStarting:
		return "starting"
	case WorkerStateAvailable:
		return "available"
	case WorkerStateBusy:
		return "busy"
	case WorkerStateUnhealthy:
		return "unhealthy"
	case WorkerStateDead:
		return "dead"
	default:
		return "unknown"
	}
}

// Worker represents a single steel-browser process.
type Worker struct {
	ID         int
	Port       int
	BinaryPath string

	mu        sync.Mutex
	cmd       *exec.Cmd
	state     WorkerState
	sessionID string // current session held by this worker
	pool      *Pool  // back-reference to the pool for Release

	// OnCrash is called when the worker crashes with an active session.
	// The callback receives the session ID so the session manager can clean up.
	OnCrash func(sessionID string)
}

// NewWorker creates a new worker instance (does not start it).
func NewWorker(id, port int, binaryPath string, pool *Pool) *Worker {
	return &Worker{
		ID:         id,
		Port:       port,
		BinaryPath: binaryPath,
		state:      WorkerStateDead,
		pool:       pool,
	}
}

// Start spawns the steel-browser process and begins monitoring it.
func (w *Worker) Start() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.state != WorkerStateDead && w.state != WorkerStateUnhealthy {
		return fmt.Errorf("worker %d already running (state=%s)", w.ID, w.state)
	}

	cmd := exec.Command(w.BinaryPath)
	cmd.Env = append(os.Environ(), fmt.Sprintf("PORT=%d", w.Port))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start worker %d: %w", w.ID, err)
	}

	w.cmd = cmd
	w.state = WorkerStateStarting
	w.sessionID = ""

	log.Printf("[worker %d] started on port %d (pid=%d)", w.ID, w.Port, cmd.Process.Pid)

	// Monitor for process exit in background
	go w.monitor()

	// Wait for the worker to become healthy
	go w.waitForReady()

	return nil
}

// monitor waits for the process to exit and handles restart.
func (w *Worker) monitor() {
	err := w.cmd.Wait()

	w.mu.Lock()
	prevSession := w.sessionID
	w.state = WorkerStateDead
	w.sessionID = ""
	w.mu.Unlock()

	if prevSession != "" {
		log.Printf("[worker %d] crashed with active session %s", w.ID, prevSession)
		// Notify session manager to clean up the stale mapping
		if w.OnCrash != nil {
			w.OnCrash(prevSession)
		}
	}
	log.Printf("[worker %d] process exited: %v — restarting in 1s", w.ID, err)

	time.Sleep(1 * time.Second)

	if err := w.Start(); err != nil {
		log.Printf("[worker %d] failed to restart: %v", w.ID, err)
	}
}

// waitForReady polls /health until the worker responds.
// run as a goroutine
func (w *Worker) waitForReady() {
	client := &http.Client{Timeout: 1 * time.Second}
	url := fmt.Sprintf("http://localhost:%d/health", w.Port)

	for i := 0; i < 30; i++ {
		resp, err := client.Get(url)
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			w.mu.Lock()
			if w.state == WorkerStateStarting {
				w.state = WorkerStateAvailable
				log.Printf("[worker %d] ready", w.ID)
			}
			w.mu.Unlock()
			// Push to the pool's available channel so queued requests can proceed
			if w.pool != nil {
				w.pool.Release(w)
			}
			return
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(200 * time.Millisecond)
	}

	log.Printf("[worker %d] failed to become ready after 6s", w.ID)
	w.mu.Lock()
	w.state = WorkerStateUnhealthy
	w.mu.Unlock()
}

// HealthCheck pings the worker's /health endpoint. Returns true if healthy.
func (w *Worker) HealthCheck() bool {
	client := &http.Client{Timeout: 2 * time.Second}
	url := fmt.Sprintf("http://localhost:%d/health", w.Port)

	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// Kill forcefully terminates the worker process.
func (w *Worker) Kill() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.cmd != nil && w.cmd.Process != nil {
		log.Printf("[worker %d] killing process (pid=%d)", w.ID, w.cmd.Process.Pid)
		_ = w.cmd.Process.Kill()
	}
}

// State returns the current worker state (thread-safe).
func (w *Worker) State() WorkerState {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state
}

// SetState updates the worker state (thread-safe).
func (w *Worker) SetState(s WorkerState) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.state = s
}

// SessionID returns the current session ID (thread-safe).
func (w *Worker) SessionID() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.sessionID
}

// SetSessionID updates the session ID and marks the worker busy/available.
// When clearing a session (id == ""), the worker is released back to the pool.
func (w *Worker) SetSessionID(id string) {
	w.mu.Lock()
	w.sessionID = id
	if id == "" {
		w.state = WorkerStateAvailable
	} else {
		w.state = WorkerStateBusy
	}
	w.mu.Unlock()

	// Release back to pool when session is cleared
	if id == "" && w.pool != nil {
		w.pool.Release(w)
	}
}

// BaseURL returns the worker's base URL.
func (w *Worker) BaseURL() string {
	return fmt.Sprintf("http://localhost:%d", w.Port)
}
