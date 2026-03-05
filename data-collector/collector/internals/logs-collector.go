package internals

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// HTTPLogEntry represents a parsed HTTP access log line
type HTTPLogEntry struct {
	Timestamp  time.Time
	Method     string
	Path       string
	StatusCode int
	BytesSent  int
	UserAgent  string
	Duration   float64 // in milliseconds
}

// JSONLogEntry represents a JSON structured log
type JSONLogEntry struct {
	Time       string `json:"time"`
	Level      string `json:"level"`
	Msg        string `json:"msg"`
	Method     string `json:"method"`
	StatusCode int    `json:"status_code"`
	Path       string `json:"path"`
	UserAgent  string `json:"user_agent"`
	Duration   string `json:"duration"`
}

// PodLogStats aggregates log statistics for a pod over a time window
type PodLogStats struct {
	PodName         string
	Timestamp       time.Time
	TotalRequests   int
	ErrorCount      int     // 5xx only (server errors)
	ClientErrors    int     // 4xx
	ServerErrors    int     // 5xx
	ErrorRate       float64 // Percentage of errors
	RequestRate     float64 // Requests per second
	AvgResponseTime float64 // Average response time in ms
	P95ResponseTime float64 // 95th percentile response time
}

// LogsCollector collects and analyzes pod logs
type LogsCollector struct {
	k8sClient *kubernetes.Clientset
	mu        sync.Mutex
	stats     map[string]*PodLogStats // key: podName
}

// Common Log Format regex pattern
// Example: ::ffff:10.244.0.1 - - [07/Feb/2026:04:08:56 +0000] "GET /hello HTTP/1.1" 200 25 "-" "python-requests/2.32.5"
var httpLogPattern = regexp.MustCompile(
	`^(\S+) \S+ \S+ \[([^\]]+)\] "(\S+) (\S+) \S+" (\d{3}) (\d+|-) "([^"]*)" "([^"]*)"`,
)

func NewLogsCollector(k8sClient *kubernetes.Clientset) *LogsCollector {
	return &LogsCollector{
		k8sClient: k8sClient,
		stats:     make(map[string]*PodLogStats),
	}
}

// ParseHTTPLog parses both JSON and Combined Log Format
func (lc *LogsCollector) ParseHTTPLog(line string) (*HTTPLogEntry, error) {
	// Try JSON format first
	if len(line) > 0 && line[0] == '{' {
		var jsonLog JSONLogEntry
		if err := json.Unmarshal([]byte(line), &jsonLog); err == nil {
			// Successfully parsed JSON
			timestamp, err := time.Parse(time.RFC3339, jsonLog.Time)
			if err != nil {
				timestamp = time.Now()
			}
			
			// Parse duration (e.g., "15ms" -> 15.0)
			duration := 0.0
			if jsonLog.Duration != "" {
				durationStr := jsonLog.Duration
				if len(durationStr) > 2 && durationStr[len(durationStr)-2:] == "ms" {
					durationStr = durationStr[:len(durationStr)-2]
					if val, err := strconv.ParseFloat(durationStr, 64); err == nil {
						duration = val
					}
				}
			}
			
			return &HTTPLogEntry{
				Timestamp:  timestamp,
				Method:     jsonLog.Method,
				Path:       jsonLog.Path,
				StatusCode: jsonLog.StatusCode,
				BytesSent:  0, // Not available in JSON format
				UserAgent:  jsonLog.UserAgent,
				Duration:   duration,
			}, nil
		}
	}
	
	// Fall back to Combined Log Format
	matches := httpLogPattern.FindStringSubmatch(line)
	if matches == nil {
		return nil, fmt.Errorf("failed to parse log line")
	}

	// Parse timestamp: [07/Feb/2026:04:08:56 +0000]
	timestamp, err := time.Parse("02/Jan/2006:15:04:05 -0700", matches[2])
	if err != nil {
		return nil, fmt.Errorf("failed to parse timestamp: %v", err)
	}

	statusCode, _ := strconv.Atoi(matches[5])
	
	bytesSent := 0
	if matches[6] != "-" {
		bytesSent, _ = strconv.Atoi(matches[6])
	}

	return &HTTPLogEntry{
		Timestamp:  timestamp,
		Method:     matches[3],
		Path:       matches[4],
		StatusCode: statusCode,
		BytesSent:  bytesSent,
		UserAgent:  matches[8],
		Duration:   0, // Not available in combined log format
	}, nil
}

// CollectPodLogs fetches and analyzes logs from a specific pod
func (lc *LogsCollector) CollectPodLogs(podName string, sinceTime time.Time) (*PodLogStats, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Fetch logs since the given time
	req := lc.k8sClient.CoreV1().Pods("default").GetLogs(podName, &v1.PodLogOptions{
		SinceTime: &metav1.Time{Time: sinceTime},
	})

	podLogs, err := req.Stream(ctx)
	if err != nil {
		return nil, fmt.Errorf("error streaming logs for pod %s: %v", podName, err)
	}
	defer podLogs.Close()

	// Parse logs and calculate statistics
	stats := &PodLogStats{
		PodName:   podName,
		Timestamp: time.Now(),
	}

	var responseTimes []float64

	scanner := bufio.NewScanner(podLogs)
	for scanner.Scan() {
		line := scanner.Text()
		entry, err := lc.ParseHTTPLog(line)
		if err != nil {
			// Skip lines that don't match (e.g., startup messages)
			continue
		}

		stats.TotalRequests++

		// Track response times
		if entry.Duration > 0 {
			responseTimes = append(responseTimes, entry.Duration)
		}

	// Classify errors (only 5xx counts as errors for self-healing)
	if entry.StatusCode >= 400 && entry.StatusCode < 500 {
		stats.ClientErrors++
		// 4xx are client errors, don't count toward ErrorCount
	} else if entry.StatusCode >= 500 {
		stats.ServerErrors++
		stats.ErrorCount++ // Only server errors count
	}

	// Calculate error rate
	if stats.TotalRequests > 0 {
		stats.ErrorRate = (float64(stats.ErrorCount) / float64(stats.TotalRequests)) * 100.0
	}

	// Calculate request rate (requests per second over the time window)
	duration := time.Since(sinceTime).Seconds()
	if duration > 0 {
		stats.RequestRate = float64(stats.TotalRequests) / duration
	}

	// Calculate average response time
	if len(responseTimes) > 0 {
		sum := 0.0
		for _, rt := range responseTimes {
			sum += rt
		}
		stats.AvgResponseTime = sum / float64(len(responseTimes))

		// Calculate p95 response time
		if len(responseTimes) > 1 {
			// Sort response times
			sortedTimes := make([]float64, len(responseTimes))
			copy(sortedTimes, responseTimes)
			// Simple bubble sort for small arrays
			for i := 0; i < len(sortedTimes); i++ {
				for j := i + 1; j < len(sortedTimes); j++ {
					if sortedTimes[i] > sortedTimes[j] {
						sortedTimes[i], sortedTimes[j] = sortedTimes[j], sortedTimes[i]
					}
				}
			}
			p95Index := int(float64(len(sortedTimes)) * 0.95)
			if p95Index >= len(sortedTimes) {
				p95Index = len(sortedTimes) - 1
			}
			stats.P95ResponseTime = sortedTimes[p95Index]
		} else {
			stats.P95ResponseTime = stats.AvgResponseTime
		}
	}

	return stats, nil
}

// CollectAllPodsLogs fetches logs from all pods in the default namespace
func (lc *LogsCollector) CollectAllPodsLogs(sinceTime time.Time) (map[string]*PodLogStats, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pods, err := lc.k8sClient.CoreV1().Pods("default").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("error fetching pods: %v", err)
	}

	allStats := make(map[string]*PodLogStats)

	for _, pod := range pods.Items {
		// Skip pods that are not running
		if pod.Status.Phase != v1.PodRunning {
			continue
		}

		stats, err := lc.CollectPodLogs(pod.Name, sinceTime)
		if err != nil {
			fmt.Printf("Warning: failed to collect logs for pod %s: %v\n", pod.Name, err)
			continue
		}

		allStats[pod.Name] = stats

		// Print summary
		if stats.AvgResponseTime > 0 {
			fmt.Printf("Pod: %s | Requests: %d | Errors: %d (%.2f%%) | Rate: %.2f req/s | Avg: %.1fms | P95: %.1fms\n",
				pod.Name, stats.TotalRequests, stats.ErrorCount, stats.ErrorRate, stats.RequestRate, stats.AvgResponseTime, stats.P95ResponseTime)
		} else {
			fmt.Printf("Pod: %s | Requests: %d | Errors: %d (%.2f%%) | Rate: %.2f req/s\n",
				pod.Name, stats.TotalRequests, stats.ErrorCount, stats.ErrorRate, stats.RequestRate)
		}
	}

	lc.mu.Lock()
	lc.stats = allStats
	lc.mu.Unlock()

	return allStats, nil
}

// GetStats returns the current statistics (thread-safe)
func (lc *LogsCollector) GetStats() map[string]*PodLogStats {
	lc.mu.Lock()
	defer lc.mu.Unlock()

	// Return a copy
	statsCopy := make(map[string]*PodLogStats)
	for k, v := range lc.stats {
		statsCopy[k] = v
	}
	return statsCopy
}

// IsHealthy determines if a pod is healthy based on error rate thresholds
func (stats *PodLogStats) IsHealthy() bool {
	// Consider unhealthy if:
	// 1. Error rate > 10%
	// 2. Server errors > 5% of requests
	if stats.ErrorRate > 10.0 {
		return false
	}

	if stats.TotalRequests > 0 {
		serverErrorRate := (float64(stats.ServerErrors) / float64(stats.TotalRequests)) * 100.0
		if serverErrorRate > 5.0 {
			return false
		}
	}

	return true
}
