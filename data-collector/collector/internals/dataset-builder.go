package internals

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// UnifiedDataPoint combines metrics and log statistics for ML training
type UnifiedDataPoint struct {
	Timestamp time.Time
	PodName   string

	// Metrics data
	CPUUsage      float64
	CPULimit      float64
	MemoryUsage   float64
	MemoryLimit   float64
	CPUPercent    float64
	MemoryPercent float64

	// Log-derived data
	TotalRequests   int
	ErrorCount      int
	ClientErrors    int
	ServerErrors    int
	ErrorRate       float64
	RequestRate     float64
	AvgResponseTime float64
	P95ResponseTime float64

	// K8s metadata
	RestartCount int32
	PodAge       float64 // Age in hours

	// Label for ML (target variable)
	IsHealthy    bool
	NeedsRestart bool
}

// DatasetBuilder combines metrics and logs to create training datasets
type DatasetBuilder struct {
	k8sClient        *kubernetes.Clientset
	metricsCollector *MetricsCollector
	logsCollector    *LogsCollector
}

func NewDatasetBuilder(k8sClient *kubernetes.Clientset, mc *MetricsCollector, lc *LogsCollector) *DatasetBuilder {
	return &DatasetBuilder{
		k8sClient:        k8sClient,
		metricsCollector: mc,
		logsCollector:    lc,
	}
}

// CollectUnifiedData gathers both metrics and log data for all pods
func (db *DatasetBuilder) CollectUnifiedData(lookbackDuration time.Duration) ([]*UnifiedDataPoint, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Get all pods
	pods, err := db.k8sClient.CoreV1().Pods("default").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("error fetching pods: %v", err)
	}

	// Collect log statistics
	sinceTime := time.Now().Add(-lookbackDuration)
	logStats, err := db.logsCollector.CollectAllPodsLogs(sinceTime)
	if err != nil {
		fmt.Printf("Warning: error collecting logs: %v\n", err)
		logStats = make(map[string]*PodLogStats) // Continue with empty log stats
	}

	var dataPoints []*UnifiedDataPoint

	for _, pod := range pods.Items {
		// Get metrics for this pod
		uCpu, lCpu, uMem, lMem := db.metricsCollector.GetPodResourceMetrics(pod.Name)

		// Calculate percentages
		cpuPercent := 0.0
		if lCpu > 0 {
			cpuPercent = (uCpu / lCpu) * 100.0
		}

		memPercent := 0.0
		if lMem > 0 {
			memPercent = (uMem / lMem) * 100.0
		}

		// Get log stats (if available)
		logStat, hasLogs := logStats[pod.Name]

		// Get restart count
		restartCount := int32(0)
		if len(pod.Status.ContainerStatuses) > 0 {
			restartCount = pod.Status.ContainerStatuses[0].RestartCount
		}

		// Calculate pod age
		podAge := time.Since(pod.CreationTimestamp.Time).Hours()

		// Create unified data point
		dp := &UnifiedDataPoint{
			Timestamp:     time.Now(),
			PodName:       pod.Name,
			CPUUsage:      uCpu,
			CPULimit:      lCpu,
			MemoryUsage:   uMem,
			MemoryLimit:   lMem,
			CPUPercent:    cpuPercent,
			MemoryPercent: memPercent,
			RestartCount:  restartCount,
			PodAge:        podAge,
		}

		// Add log statistics if available
		if hasLogs {
			dp.TotalRequests = logStat.TotalRequests
			dp.ErrorCount = logStat.ErrorCount
			dp.ClientErrors = logStat.ClientErrors
			dp.ServerErrors = logStat.ServerErrors
			dp.ErrorRate = logStat.ErrorRate
			dp.RequestRate = logStat.RequestRate
			dp.AvgResponseTime = logStat.AvgResponseTime
			dp.P95ResponseTime = logStat.P95ResponseTime
		}

		// Determine health status (labels for ML)
		dp.IsHealthy = db.determineHealth(dp)
		dp.NeedsRestart = db.determineNeedsRestart(dp)

		dataPoints = append(dataPoints, dp)
	}

	return dataPoints, nil
}

// determineHealth applies rules to label data as healthy/unhealthy
func (db *DatasetBuilder) determineHealth(dp *UnifiedDataPoint) bool {
	// Unhealthy conditions:
	// 1. High error rate (> 10%)
	// 2. High resource usage (CPU > 90% or Memory > 90%)
	// 3. Server errors present
	// 4. Recent restarts (> 2)

	if dp.ErrorRate > 10.0 {
		return false
	}

	if dp.ServerErrors > 0 {
		return false
	}

	if dp.CPUPercent > 90.0 || dp.MemoryPercent > 90.0 {
		return false
	}

	if dp.RestartCount > 2 {
		return false
	}

	return true
}

// determineNeedsRestart determines if a pod should be restarted
func (db *DatasetBuilder) determineNeedsRestart(dp *UnifiedDataPoint) bool {
	// Should restart if:
	// 1. High error rate + high resource usage
	// 2. Memory leak pattern (memory > 85% and growing)
	// 3. Multiple recent errors with server errors

	if dp.ErrorRate > 15.0 && (dp.CPUPercent > 80.0 || dp.MemoryPercent > 80.0) {
		return true
	}

	if dp.MemoryPercent > 85.0 {
		return true
	}

	if dp.ServerErrors > 5 && dp.ErrorRate > 20.0 {
		return true
	}

	return false
}

// WriteToCSV writes unified data points to a CSV file
func (db *DatasetBuilder) WriteToCSV(dataPoints []*UnifiedDataPoint, filename string) error {
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("error opening file: %v", err)
	}
	defer file.Close()

	// Check if file is empty (need to write header)
	fileInfo, _ := file.Stat()
	writeHeader := fileInfo.Size() == 0

	writer := csv.NewWriter(file)
	defer writer.Flush()

	// Write header if needed
	if writeHeader {
		header := []string{
			"timestamp",
			"pod_name",
			"cpu_usage",
			"cpu_limit",
			"cpu_percent",
			"memory_usage",
			"memory_limit",
			"memory_percent",
			"total_requests",
			"error_count",
			"client_errors",
			"server_errors",
			"error_rate",
			"request_rate",
			"avg_response_time",
			"p95_response_time",
			"restart_count",
			"pod_age_hours",
			"is_healthy",
			"needs_restart",
		}
		if err := writer.Write(header); err != nil {
			return fmt.Errorf("error writing header: %v", err)
		}
	}

	// Write data points
	for _, dp := range dataPoints {
		row := []string{
			dp.Timestamp.Format(time.RFC3339),
			dp.PodName,
			fmt.Sprintf("%.6f", dp.CPUUsage),
			fmt.Sprintf("%.6f", dp.CPULimit),
			fmt.Sprintf("%.2f", dp.CPUPercent),
			fmt.Sprintf("%.0f", dp.MemoryUsage),
			fmt.Sprintf("%.0f", dp.MemoryLimit),
			fmt.Sprintf("%.2f", dp.MemoryPercent),
			fmt.Sprintf("%d", dp.TotalRequests),
			fmt.Sprintf("%d", dp.ErrorCount),
			fmt.Sprintf("%d", dp.ClientErrors),
			fmt.Sprintf("%d", dp.ServerErrors),
			fmt.Sprintf("%.2f", dp.ErrorRate),
			fmt.Sprintf("%.2f", dp.RequestRate),
			fmt.Sprintf("%.2f", dp.AvgResponseTime),
			fmt.Sprintf("%.2f", dp.P95ResponseTime),
			fmt.Sprintf("%d", dp.RestartCount),
			fmt.Sprintf("%.2f", dp.PodAge),
			fmt.Sprintf("%t", dp.IsHealthy),
			fmt.Sprintf("%t", dp.NeedsRestart),
		}
		if err := writer.Write(row); err != nil {
			return fmt.Errorf("error writing row: %v", err)
		}
	}

	return nil
}

// CreateBackupDataset creates a comprehensive historical dataset
func (db *DatasetBuilder) CreateBackupDataset(filename string) error {
	// Collect current data
	dataPoints, err := db.CollectUnifiedData(5 * time.Minute)
	if err != nil {
		return fmt.Errorf("error collecting unified data: %v", err)
	}

	// Sort by timestamp
	sort.Slice(dataPoints, func(i, j int) bool {
		return dataPoints[i].Timestamp.Before(dataPoints[j].Timestamp)
	})

	// Write to CSV
	if err := db.WriteToCSV(dataPoints, filename); err != nil {
		return fmt.Errorf("error writing to CSV: %v", err)
	}

	fmt.Printf("✓ Wrote %d data points to %s\n", len(dataPoints), filename)
	return nil
}
