package internals

import (
	"bufio"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"time"

	"github.com/prometheus/client_golang/api"
	promV1 "github.com/prometheus/client_golang/api/prometheus/v1"
	k8sV1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/prometheus/common/model"
	"github.com/robfig/cron/v3"
	"k8s.io/client-go/kubernetes"
)

type MetricsCollector struct {
	promClient promV1.API
	k8sClient  *kubernetes.Clientset
	cron       *cron.Cron
}

func NewMetricsCollector(k8sClient *kubernetes.Clientset) *MetricsCollector {
	cron := cron.New(cron.WithSeconds())
	client, _ := api.NewClient(api.Config{
		Address: "http://localhost:9090",
	})
	apiV1 := promV1.NewAPI(client)

	return &MetricsCollector{
		promClient: apiV1,
		cron:       cron,
		k8sClient:  k8sClient,
	}
}

func (this *MetricsCollector) Watch() {
	// this.watchDeploymentMetrics()
	this.watchPodMetrics()
	this.CreateMetricsBackup("my-app")
	this.cron.Start()
}

type MetricRow struct {
	Timestamp string
	PodName   string
	UCpu      string
	LCpu      string
	UMem      string
	LMem      string
}

func (this *MetricsCollector) CreateMetricsBackup(deploymentName string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Key is "timestamp_podname"
	dataMap := make(map[string]*MetricRow)

	queries := []struct {
		Key   string
		Query string
	}{
		{"uCpu", fmt.Sprintf("sum(rate(container_cpu_usage_seconds_total{pod=~'%s-.*'}[5m])) by (pod)", deploymentName)},
		{"uMem", fmt.Sprintf("sum(container_memory_working_set_bytes{pod=~'%s-.*'}) by (pod)", deploymentName)},
		{"lCpu", fmt.Sprintf("sum(kube_pod_container_resource_limits{pod=~'%s-.*', resource='cpu'}) by (pod)", deploymentName)},
		{"lMem", fmt.Sprintf("sum(kube_pod_container_resource_limits{pod=~'%s-.*', resource='memory'}) by (pod)", deploymentName)},
	}

	dateRange := promV1.Range{
		Start: time.Now().Add(-7 * 24 * time.Hour).UTC(),
		End:   time.Now().UTC(),
		Step:  10 * time.Minute, // Increased step to keep file size sane
	}

	for _, q := range queries {
		result, _, err := this.promClient.QueryRange(ctx, q.Query, dateRange)
		if err != nil {
			fmt.Printf("Query error: %v\n", err)
			continue
		}

		matrix := result.(model.Matrix)
		for _, series := range matrix {
			podName := string(series.Metric["pod"])

			for _, sample := range series.Values {
				ts := sample.Timestamp.Time().UTC().Format(time.RFC3339)
				mapKey := ts + "_" + podName

				// Initialize row if it doesn't exist
				if _, exists := dataMap[mapKey]; !exists {
					dataMap[mapKey] = &MetricRow{Timestamp: ts, PodName: podName}
				}

				// Assign value to the correct column
				valStr := sample.Value.String()
				switch q.Key {
				case "uCpu":
					dataMap[mapKey].UCpu = valStr
				case "uMem":
					dataMap[mapKey].UMem = valStr
				case "lCpu":
					dataMap[mapKey].LCpu = valStr
				case "lMem":
					dataMap[mapKey].LMem = valStr
				}
			}
		}
	}

	keys := make([]string, 0, len(dataMap))
	for k := range dataMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Convert Map to CSV slice
	allValues := [][]string{{"date", "pod_name", "cpu_usage", "cpu_limit", "memory_usage", "memory_limit"}}
	for _, k := range keys {
		row := dataMap[k]
		allValues = append(allValues, []string{
			row.Timestamp, row.PodName, row.UCpu, row.LCpu, row.UMem, row.LMem,
		})
	}

	// Write to file
	file, _ := os.Create("training_data.csv")
	defer file.Close()
	writer := csv.NewWriter(file)
	writer.WriteAll(allValues)
	fmt.Println("Backup complete: training_data.csv")
}

func (this *MetricsCollector) watchDeploymentMetrics() {
	_, err := this.cron.AddFunc("@every 30s", func() {
		ctx := context.Background()
		deps, err := this.k8sClient.AppsV1().Deployments("default").List(ctx, metav1.ListOptions{})

		if err != nil {
			fmt.Printf("Failed to list pods: %v", err)
			return
		}
		for _, d := range deps.Items {
			// fmt.Printf("Pod: %s\n", pod.Name)
			containerName := d.Spec.Template.Spec.Containers[0].Name
			uCpu, lCpu, uMem, lMem := this.getDeploymentResourceMetrics(containerName)

			cpuPercent := 0.0
			if lCpu > 0 {
				cpuPercent = uCpu / lCpu
			}

			memPercent := 0.0
			if lMem > 0 {
				memPercent = uMem / lMem
			}
			fmt.Printf("App: %s | CPU: %.2f%% | Mem: %.2f%%\n", containerName, cpuPercent*100, memPercent*100)
		}
	})
	if err != nil {
		fmt.Printf("Failed to get metrics from Prometheus: %v\n", err)
		return
	}
}

/*
 */
func (this *MetricsCollector) watchPodMetrics() {
	_, err := this.cron.AddFunc("@every 30s", func() {
		ctx := context.Background()
		// var labelSelector string
		// if len(deploymentName) != 0 {
		// 	labelSelector = deploymentName
		// }
		pods, err := this.k8sClient.CoreV1().Pods("default").List(ctx, metav1.ListOptions{})

		if err != nil {
			fmt.Printf("Failed to list pods: %v", err)
			return
		}

		for _, pod := range pods.Items {
			containerName := pod.Name
			uCpu, lCpu, uMem, lMem := this.getPodResourceMetrics(containerName)

			cpuPercent := 0.0
			if lCpu > 0 {
				cpuPercent = uCpu / lCpu
			}

			memPercent := 0.0
			if lMem > 0 {
				memPercent = uMem / lMem
			}
			fmt.Printf("Pod: %s | CPU: %.2f%% | Mem: %.2f%%\n", containerName, cpuPercent*100, memPercent*100)
		}
	})
	if err != nil {
		fmt.Printf("Failed to get metrics from Prometheus: %v\n", err)
		return
	}
}

func (this *MetricsCollector) getDeploymentResourceMetrics(containerName string) (usageCpu, limitCpu, usageMem, limitMem float64) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	queries := map[string]string{
		"uCpu": fmt.Sprintf("sum(rate(container_cpu_usage_seconds_total{pod=~'%s-.*'}[2m]))", containerName),
		"lCpu": fmt.Sprintf("sum(kube_pod_container_resource_limits{pod=~'%s-.*', resource='cpu'})", containerName),
		"uMem": fmt.Sprintf("sum(container_memory_working_set_bytes{pod=~'%s-.*'})", containerName),
		"lMem": fmt.Sprintf("sum(kube_pod_container_resource_limits{pod=~'%s-.*', resource='memory'})", containerName),
	}

	results := make(map[string]float64)
	for key, q := range queries {
		val, _, err := this.promClient.Query(ctx, q, time.Now())
		if err != nil {
			fmt.Printf("Error querying Prometheus: %v", err)
			results[key] = 0
			continue
		}
		if vector, ok := val.(model.Vector); ok && len(vector) > 0 {
			// fmt.Printf("%s\n", (vector[0].Value.String()))
			results[key] = float64(vector[0].Value)
		} else {
			results[key] = 0
		}
	}
	return results["uCpu"], results["lCpu"], results["uMem"], results["lMem"]
}

func (this *MetricsCollector) getPodResourceMetrics(containerName string) (usageCpu, limitCpu, usageMem, limitMem float64) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	queries := map[string]string{
		"uCpu": fmt.Sprintf("sum(rate(container_cpu_usage_seconds_total{pod='%s'}[2m]))", containerName),
		"lCpu": fmt.Sprintf("sum(kube_pod_container_resource_limits{pod='%s', resource='cpu'})", containerName),
		"uMem": fmt.Sprintf("sum(container_memory_working_set_bytes{pod='%s'})", containerName),
		"lMem": fmt.Sprintf("sum(kube_pod_container_resource_limits{pod='%s', resource='memory'})", containerName),
	}
	results := make(map[string]float64)
	for key, q := range queries {
		val, _, err := this.promClient.Query(ctx, q, time.Now())
		if err != nil {
			fmt.Printf("Query error for %s: %v\n", key, err)
			continue
		}
		if vector, ok := val.(model.Vector); ok && len(vector) > 0 {
			// fmt.Printf("{%s: %f}\n", key, vector[0].Value)
			// println(vector)
			results[key] = float64(vector[0].Value)
		}
	}
	return results["uCpu"], results["lCpu"], results["uMem"], results["lMem"]
}
