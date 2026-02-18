package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

const workerRequestTimeout = 5 * time.Second

var httpClient = &http.Client{
	Timeout: workerRequestTimeout,
}

// forwardCreateSession sends POST /sessions to the worker and returns the response body.
func forwardCreateSession(worker *Worker, body []byte) ([]byte, int, error) {
	url := fmt.Sprintf("%s/sessions", worker.BaseURL())

	ctx, cancel := context.WithTimeout(context.Background(), workerRequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		log.Printf("[proxy] POST /sessions to worker %d failed: %v", worker.ID, err)
		return nil, 0, fmt.Errorf("forward to worker %d: %w", worker.ID, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", err)
	}

	return respBody, resp.StatusCode, nil
}

// forwardGetSession sends GET /sessions/:id to the worker.
func forwardGetSession(worker *Worker, sessionID string) ([]byte, int, error) {
	url := fmt.Sprintf("%s/sessions/%s", worker.BaseURL(), sessionID)

	ctx, cancel := context.WithTimeout(context.Background(), workerRequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		log.Printf("[proxy] GET /sessions/%s to worker %d failed: %v", sessionID, worker.ID, err)
		return nil, 0, fmt.Errorf("forward to worker %d: %w", worker.ID, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", err)
	}

	return respBody, resp.StatusCode, nil
}

// deleteSessionFromWorker sends DELETE /sessions/:id to the worker.
func deleteSessionFromWorker(worker *Worker, sessionID string) (int, error) {
	url := fmt.Sprintf("%s/sessions/%s", worker.BaseURL(), sessionID)

	ctx, cancel := context.WithTimeout(context.Background(), workerRequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return 0, fmt.Errorf("create request: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		log.Printf("[proxy] DELETE /sessions/%s to worker %d failed: %v", sessionID, worker.ID, err)
		return 0, fmt.Errorf("forward to worker %d: %w", worker.ID, err)
	}
	defer resp.Body.Close()

	// Drain body to allow connection reuse
	_, _ = io.Copy(io.Discard, resp.Body)

	return resp.StatusCode, nil
}
