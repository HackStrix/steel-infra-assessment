package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// Pool manages a set of workers with request queuing.
// When all workers are busy, callers block until one becomes available.
type Pool struct {
	mu      sync.RWMutex
	workers []*Worker

	// available is a buffered channel used as a semaphore.
	// Workers are pushed onto it when free, and popped off when claimed.
	available chan *Worker
}

// NewPool creates a pool of workers and starts them all.
func NewPool(count int, binaryPath string, basePort int) (*Pool, error) {
	p := &Pool{
		workers:   make([]*Worker, count),
		available: make(chan *Worker, count),
	}

	for i := 0; i < count; i++ {
		port := basePort + i
		w := NewWorker(i, port, binaryPath, p)
		if err := w.Start(); err != nil {
			return nil, fmt.Errorf("failed to start worker %d: %w", i, err)
		}
		p.workers[i] = w
	}

	// Start background health checker
	go p.healthCheckLoop()

	return p, nil
}

// Release returns a worker to the available pool.
// Called after a session is deleted, expired, or the worker is restarted.
func (p *Pool) Release(w *Worker) {
	// Non-blocking send — if channel is full, worker is already "available"
	select {
	case p.available <- w:
		log.Printf("[pool] worker %d released back to pool", w.ID)
	default:
		log.Printf("[pool] worker %d release skipped (already in pool)", w.ID)
	}
}

// Acquire blocks until a worker is available or the context is canceled.
// This is the queuing mechanism — callers wait in line.
func (p *Pool) Acquire(ctx context.Context) (*Worker, error) {
		select {
		case w := <-p.available:
			log.Printf("[pool] worker %d acquired from pool", w.ID)
			return w, nil
		case <-ctx.Done():
			return nil, fmt.Errorf("timed out waiting for available worker: %w", ctx.Err())
		}
	}

// FindBySession returns the worker that holds the given session ID.
func (p *Pool) FindBySession(sessionID string) (*Worker, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, w := range p.workers {
		if w.SessionID() == sessionID {
			return w, true
		}
	}
	return nil, false
}

// Workers returns all workers in the pool (for status/debugging).
func (p *Pool) Workers() []*Worker {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*Worker, len(p.workers))
	copy(out, p.workers)
	return out
}

// QueueDepth returns how many workers are currently available.
func (p *Pool) QueueDepth() int {
	return len(p.available)
}

// healthCheckLoop periodically checks worker health and restarts unhealthy ones.
func (p *Pool) healthCheckLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		p.mu.RLock()
		workers := make([]*Worker, len(p.workers))
		copy(workers, p.workers)
		p.mu.RUnlock()

		for _, w := range workers {
			state := w.State()
			if state == WorkerStateDead || state == WorkerStateStarting {
				continue
			}

			if !w.HealthCheck() {
				log.Printf("[pool] worker %d failed health check (state=%s) — killing", w.ID, state)
				w.Kill() // monitor goroutine will handle restart
			}
		}
	}
}

// Shutdown kills all workers.
func (p *Pool) Shutdown() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, w := range p.workers {
		w.Kill()
	}
	log.Println("[pool] all workers shut down")
}
