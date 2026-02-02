package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/MEDATTA0/collector/internals"
	"github.com/MEDATTA0/collector/pkg"
	"go.uber.org/zap"
)

type GetHealthResponseDto struct {
	Message string `json:"message"`
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(200)
	w.Header().Set("Content-Type", "application/json")

	response := GetHealthResponseDto{Message: "Ok"}
	json.NewEncoder(w).Encode(response)

}

func main() {
	devLogger, _ := zap.NewDevelopment()
	defer devLogger.Sync()

	// sugar := devLogger.Sugar()

	fmt.Printf("Hello world from Go\n")
	PORT := 1323
	server := http.NewServeMux()
	k8sClient, err := pkg.NewK8sClient()
	if err != nil {
		panic("Failed to initialize K8sClient")
	}
	k8sRouter := internals.NewK8sRouter(k8sClient)
	mlTalker := internals.NewMLTalker(k8sClient)
	metricsCollector := internals.NewMetricsCollector(k8sClient)
	mlTalker.Watch()
	metricsCollector.Watch()

	server.HandleFunc("/health", handleHealth)
	server.Handle("/k8s/", http.StripPrefix("/k8s", k8sRouter))

	fmt.Printf("Server running on %d\n", PORT)
	err = http.ListenAndServe(fmt.Sprintf(":%d", PORT), server)
	if err != nil {
		fmt.Printf("Error when starting the server: %s\n", err)
	}
}
