package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"time"
)

// Pool manages a set of workers with request queuing.
// When all workers are busy, callers block until one becomes available.
// The pool auto-scales between min and max workers based on demand.
type Pool struct {
	mu      sync.RWMutex
	workers []*Worker

	// available is a buffered channel used as a semaphore.
	// Workers are pushed onto it when free, and popped off when claimed.
	available chan *Worker

	min         int
	max         int
	nextID      int    // monotonic counter, never reused
	pendingAdds int    // workers currently starting up but not yet in the slice
	binaryPath  string // path to the steel-browser binary

	// CrashHandler is called when a worker crashes with an active session.
	// Set this after pool creation to wire up session manager cleanup.
	// It is also applied automatically to any worker added during scale-up.
	CrashHandler func(sessionID string)
}

// NewPool creates a pool of min workers. Each worker is assigned a port by
// the OS, so no port range configuration is needed.
func NewPool(min, max int, binaryPath string) (*Pool, error) {
	p := &Pool{
		workers:    make([]*Worker, 0, max),
		available:  make(chan *Worker, max),
		min:        min,
		max:        max,
		nextID:     min,
		binaryPath: binaryPath,
	}

	for i := 0; i < min; i++ {
		port, err := findFreePort()
		if err != nil {
			return nil, fmt.Errorf("failed to get free port for worker %d: %w", i, err)
		}
		w := NewWorker(i, port, binaryPath, p)
		if err := w.Start(); err != nil {
			return nil, fmt.Errorf("failed to start worker %d: %w", i, err)
		}
		p.workers = append(p.workers, w)
	}

	// Start background health checker and auto-scaler
	go p.healthCheckLoop()
	go p.scaleLoop()

	return p, nil
}

// Release returns a worker to the available pool.
// Called after a session is deleted, expired, or the worker is restarted.
func (p *Pool) Release(w *Worker) {
	// Non-blocking send — if channel is full, worker is already "available"
	select {
	case p.available <- w:
		log.Printf("[pool] :%-5d returned to pool (available: %d)", w.Port, len(p.available))
	default:
		log.Printf("[pool] :%-5d release skipped — already in pool", w.Port)
	}
}

// Acquire blocks until a worker is available or the context is canceled.
// If all workers are busy and the pool has room to grow, a new worker is
// spawned asynchronously before blocking so it may arrive quickly.
func (p *Pool) Acquire(ctx context.Context) (*Worker, error) {
	// Trigger a scale-up if the pool is fully occupied and below the ceiling.
	// Use pendingAdds alongside len(workers) so we don't fire redundant goroutines
	// when multiple requests arrive simultaneously and workers are still starting.
	if len(p.available) == 0 {
		p.mu.RLock()
		total := len(p.workers) + p.pendingAdds
		p.mu.RUnlock()
		if total < p.max {
			log.Printf("[pool] all workers busy — scaling up (workers: %d → %d/%d)", total, total+1, p.max)
			go p.addWorker()
		}
	}

	select {
	case w := <-p.available:
		log.Printf("[pool] :%-5d acquired (available: %d)", w.Port, len(p.available))
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

// Min returns the minimum number of workers the pool will maintain.
func (p *Pool) Min() int { return p.min }

// Max returns the maximum number of workers the pool may scale up to.
func (p *Pool) Max() int { return p.max }

// addWorker creates, starts, and registers a new worker during scale-up.
// The OS assigns a free port; no port tracking needed.
// pendingAdds is incremented before the lock is released so that concurrent
// calls to addWorker see the correct in-flight count and cannot overshoot max.
func (p *Pool) addWorker() {
	p.mu.Lock()
	if len(p.workers)+p.pendingAdds >= p.max {
		p.mu.Unlock()
		return
	}
	id := p.nextID
	p.nextID++
	p.pendingAdds++ // reserve the slot before releasing the lock
	p.mu.Unlock()

	port, err := findFreePort()
	if err != nil {
		log.Printf("[pool] scale-up failed: could not get free port — %v", err)
		p.mu.Lock()
		p.pendingAdds--
		p.mu.Unlock()
		return
	}

	w := NewWorker(id, port, p.binaryPath, p)
	if p.CrashHandler != nil {
		w.OnCrash = p.CrashHandler
	}

	if err := w.Start(); err != nil {
		log.Printf("[pool] scale-up failed: port=%d — %v", port, err)
		p.mu.Lock()
		p.pendingAdds--
		p.mu.Unlock()
		return
	}

	p.mu.Lock()
	p.workers = append(p.workers, w)
	p.pendingAdds--
	count := len(p.workers)
	p.mu.Unlock()

	log.Printf("[pool] scale-up: :%-5d started (workers: %d/%d)", port, count, p.max)
}

// findFreePort asks the OS for an available TCP port by binding to :0.
func findFreePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// scaleLoop ticks every 10 s and removes idle workers above the minimum.
// Uses a consecutive-idle-tick counter to avoid thrashing — a worker is only
// removed after 2 ticks (20 s) of sustained idleness.
func (p *Pool) scaleLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	idleTicks := 0
	for range ticker.C {
		p.mu.RLock()
		count := len(p.workers)
		p.mu.RUnlock()

		available := len(p.available)

		if available > 0 && count > p.min {
			idleTicks++
		} else {
			idleTicks = 0
		}

		if idleTicks >= 2 {
			p.removeIdleWorker()
			idleTicks = 0
		}
	}
}

// removeIdleWorker grabs one idle worker from the available channel and shuts it down.
// The worker is drained before being killed so monitor() does not restart it.
func (p *Pool) removeIdleWorker() {
	select {
	case w := <-p.available:
		p.mu.Lock()
		for i, existing := range p.workers {
			if existing == w {
				p.workers = append(p.workers[:i], p.workers[i+1:]...)
				break
			}
		}
		count := len(p.workers)
		p.mu.Unlock()

		w.Drain()
		w.Kill()

		log.Printf("[pool] scale-down: :%-5d removed (workers: %d/%d)", w.Port, count, p.max)
	default:
		// No idle worker available right now — skip
	}
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
				log.Printf("[pool] :%-5d failed health check (state=%s) — killing", w.Port, state)
				w.Kill() // monitor goroutine will handle restart
			}
		}
	}
}

// Shutdown kills all workers. Workers are drained first so monitor()
// goroutines do not attempt a restart after the process exits.
func (p *Pool) Shutdown() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, w := range p.workers {
		w.Drain()
		w.Kill()
	}
	log.Println("[pool] all workers shut down")
}
