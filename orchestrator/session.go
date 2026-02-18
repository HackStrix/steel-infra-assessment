package main

import (
	"log"
	"sync"
	"time"
)

const sessionTTL = 60 * time.Second

// SessionEntry tracks a session's mapping to a worker and its last access time.
type SessionEntry struct {
	SessionID    string
	Worker       *Worker
	LastAccessed time.Time
}

// SessionManager handles session-to-worker mapping and TTL expiration.
type SessionManager struct {
	mu       sync.RWMutex
	sessions map[string]*SessionEntry
}

// NewSessionManager creates a new SessionManager and starts the TTL sweeper.
func NewSessionManager() (*SessionManager, error) {
	sm := &SessionManager{
		sessions: make(map[string]*SessionEntry),
	}
	// starting ttlsweeper as goroutine
	go sm.ttlSweeper()
	return sm, nil
}

// Add registers a new session mapping.
func (sm *SessionManager) Add(sessionID string, worker *Worker) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.sessions[sessionID] = &SessionEntry{
		SessionID:    sessionID,
		Worker:       worker,
		LastAccessed: time.Now(),
	}
	log.Printf("[session] registered session %s → worker %d", sessionID, worker.ID)
}

// Get looks up a session, updates its last access time, and returns the worker.
// Returns nil if the session doesn't exist.
func (sm *SessionManager) Get(sessionID string) *Worker {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	entry, ok := sm.sessions[sessionID]
	if !ok {
		return nil
	}

	entry.LastAccessed = time.Now()
	return entry.Worker
}

// Remove deletes a session mapping and frees the worker.
func (sm *SessionManager) Remove(sessionID string) *Worker {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	entry, ok := sm.sessions[sessionID]
	if !ok {
		return nil
	}

	delete(sm.sessions, sessionID)
	return entry.Worker
}

// ttlSweeper runs every 5 seconds as goroutine and expires stale sessions.
func (sm *SessionManager) ttlSweeper() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		sm.expireStale()
	}
}

// expireStale removes sessions that have exceeded the TTL.
func (sm *SessionManager) expireStale() {
	sm.mu.Lock()
	var expired []*SessionEntry
	for id, entry := range sm.sessions {
		if time.Since(entry.LastAccessed) > sessionTTL {
			expired = append(expired, entry)
			delete(sm.sessions, id)
		}
	}
	sm.mu.Unlock()

	// Delete expired sessions from their workers (outside the lock)
	for _, entry := range expired {
		log.Printf("[session] TTL expired for session %s (worker %d)", entry.SessionID, entry.Worker.ID)
		deleteSessionFromWorker(entry.Worker, entry.SessionID)
		entry.Worker.SetSessionID("")
	}
}

// Count returns the number of active sessions.
func (sm *SessionManager) Count() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return len(sm.sessions)
}
