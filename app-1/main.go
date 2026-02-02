package main

import (
	"fmt"
	"net/http"
	"time"
)

func greet(w http.ResponseWriter, r *http.Request) {
	i := 1e7
	for i >= 1 {
		i--
	}
	w.WriteHeader(http.StatusAccepted)
	w.Header().Set("Content-Type", "plain/text")
	fmt.Fprintf(w, "Hello World! %s", time.Now())
}

func main() {
	const PORT = 8080
	mux := http.NewServeMux()
	middlewareStack := CreateMiddlewareStack(LoggingMiddleware)

	mux.HandleFunc("/hello", greet)
	fmt.Printf("Server running on port %d\n", PORT)
	http.ListenAndServe(fmt.Sprintf(":%d", PORT), middlewareStack(mux))
}
