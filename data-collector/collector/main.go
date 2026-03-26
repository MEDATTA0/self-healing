package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

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

type CollectResponseDto struct {
	Message    string `json:"message"`
	DataPoints int    `json:"data_points"`
}

var globalDatasetBuilder *internals.DatasetBuilder
var globalMLTalker *internals.MLTalker

func handleCollect(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if globalDatasetBuilder == nil {
		w.WriteHeader(500)
		json.NewEncoder(w).Encode(map[string]string{"error": "Dataset builder not initialized"})
		return
	}

	fmt.Println("Manual data collection triggered...")
	dataPoints, err := globalDatasetBuilder.CollectUnifiedData(2 * time.Minute)
	if err != nil {
		w.WriteHeader(500)
		json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("Failed to collect data: %v", err)})
		return
	}

	err = globalDatasetBuilder.WriteToCSV(dataPoints, "ml_training_dataset.csv")
	if err != nil {
		w.WriteHeader(500)
		json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("Failed to write CSV: %v", err)})
		return
	}

	if globalMLTalker != nil {
		globalMLTalker.EvaluateAndHeal(dataPoints)
	}

	fmt.Printf("✓ Manual collection: %d unified data points\n", len(dataPoints))
	w.WriteHeader(200)
	response := CollectResponseDto{
		Message:    "Data collected successfully",
		DataPoints: len(dataPoints),
	}
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
	logsCollector := internals.NewLogsCollector(k8sClient)
	datasetBuilder := internals.NewDatasetBuilder(k8sClient, metricsCollector, logsCollector)
	globalDatasetBuilder = datasetBuilder // Set global for HTTP handler
	globalMLTalker = mlTalker

	mlTalker.Watch()
	metricsCollector.Watch()

	// Collect unified data every 2 minutes and append to dataset
	go func() {
		// Collect immediately on startup
		time.Sleep(10 * time.Second) // Wait for metrics to be available
		fmt.Println("Starting initial data collection...")
		dataPoints, err := datasetBuilder.CollectUnifiedData(2 * time.Minute)
		if err != nil {
			fmt.Printf("Error collecting initial data: %v\n", err)
		} else {
			err = datasetBuilder.WriteToCSV(dataPoints, "ml_training_dataset.csv")
			if err != nil {
				fmt.Printf("Error writing initial dataset: %v\n", err)
			} else {
				fmt.Printf("✓ Initial collection: %d unified data points\n", len(dataPoints))
				mlTalker.EvaluateAndHeal(dataPoints)
			}
		}

		// Then collect every 2 minutes
		ticker := time.NewTicker(2 * time.Minute)
		defer ticker.Stop()

		for range ticker.C {
			dataPoints, err := datasetBuilder.CollectUnifiedData(2 * time.Minute)
			if err != nil {
				fmt.Printf("Error collecting unified data: %v\n", err)
				continue
			}

			err = datasetBuilder.WriteToCSV(dataPoints, "ml_training_dataset.csv")
			if err != nil {
				fmt.Printf("Error writing dataset: %v\n", err)
			} else {
				fmt.Printf("✓ Collected %d unified data points\n", len(dataPoints))
				mlTalker.EvaluateAndHeal(dataPoints)
			}
		}
	}()

	server.HandleFunc("/health", handleHealth)
	server.HandleFunc("/collect", handleCollect)
	server.Handle("/k8s/", http.StripPrefix("/k8s", k8sRouter))

	fmt.Printf("Server running on %d\n", PORT)
	err = http.ListenAndServe(fmt.Sprintf(":%d", PORT), server)
	if err != nil {
		fmt.Printf("Error when starting the server: %s\n", err)
	}
}
