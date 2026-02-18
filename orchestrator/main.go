package main

import (
	"fmt"
	"net/http"
)

func main() {
	mux := http.NewServeMux()

	// TODO: POST /sessions - create a session (route to available worker)
	// TODO: GET /sessions/:id - get session (route to correct worker)
	// TODO: DELETE /sessions/:id - delete session

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	fmt.Println("Orchestrator listening on :8080")
	if err := http.ListenAndServe(":8080", mux); err != nil {
		fmt.Printf("Failed to start server: %v\n", err)
	}
}
