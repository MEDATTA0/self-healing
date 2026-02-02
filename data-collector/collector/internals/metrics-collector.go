package internals

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/api"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/prometheus/common/model"
	"github.com/robfig/cron/v3"
	"k8s.io/client-go/kubernetes"
)

type MetricsCollector struct {
	promClient v1.API
	k8sClient  *kubernetes.Clientset
	cron       *cron.Cron
}

func NewMetricsCollector(k8sClient *kubernetes.Clientset) *MetricsCollector {
	cron := cron.New(cron.WithSeconds())
	client, _ := api.NewClient(api.Config{
		Address: "http://localhost:9090",
	})
	apiV1 := v1.NewAPI(client)

	return &MetricsCollector{
		promClient: apiV1,
		cron:       cron,
		k8sClient:  k8sClient,
	}
}

func (this *MetricsCollector) Watch() {
	// this.watchDeploymentMetrics()
	this.watchPodMetrics()
	this.cron.Start()
}

func (this *MetricsCollector) watchDeploymentMetrics() {
	_, err := this.cron.AddFunc("@every 10s", func() {
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

func (this *MetricsCollector) watchPodMetrics() {
	_, err := this.cron.AddFunc("@every 10s", func() {
		ctx := context.Background()
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
