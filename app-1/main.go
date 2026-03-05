package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type HealthResponseDto struct {
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
}

func greet(w http.ResponseWriter, r *http.Request) {
	i := 1e4
	for i >= 1 {
		i--
	}
	response := &HealthResponseDto{Message: "Hello World", Timestamp: time.Now()}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
	// fmt.Fprintf(w, "Hello World! %s", time.Now())
}

func getError(w http.ResponseWriter, r *http.Request) {
	i := 1e3
	for i >= 1 {
		i--
	}
	response := &HealthResponseDto{Message: "Something went wrong, please try later"}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	json.NewEncoder(w).Encode(response)
}

func main() {
	const PORT = 8080
	mux := http.NewServeMux()
	middlewareStack := CreateMiddlewareStack(LoggingMiddleware)

	mux.HandleFunc("/hello", greet)
	mux.HandleFunc("/error", getError)
	fmt.Printf("Server running on port %d\n", PORT)
	http.ListenAndServe(fmt.Sprintf(":%d", PORT), middlewareStack(mux))
}
