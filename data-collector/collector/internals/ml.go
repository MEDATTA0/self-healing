package internals

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type MLTalker struct {
	k8sClient  *kubernetes.Clientset
	apiURL     string
	httpClient *http.Client

	mu          sync.Mutex
	cooldowns   map[string]time.Time
	cooldownFor time.Duration
}

func NewMLTalker(k8sClient *kubernetes.Clientset) *MLTalker {
	apiURL := os.Getenv("ML_API_URL")
	if apiURL == "" {
		apiURL = "http://localhost:8010"
	}

	return &MLTalker{
		k8sClient:   k8sClient,
		apiURL:      apiURL,
		httpClient:  &http.Client{Timeout: 10 * time.Second},
		cooldowns:   make(map[string]time.Time),
		cooldownFor: 3 * time.Minute,
	}
}

type MLPredictResponse struct {
	HealthState            string  `json:"health_state"`
	HealthConfidence       float64 `json:"health_confidence"`
	ActionModelDecision    string  `json:"action_model_decision"`
	ActionConfidence       float64 `json:"action_confidence"`
	FinalRecommendedAction string  `json:"final_recommended_action"`
}

// Watch remains for backward compatibility. Remediation is triggered by EvaluateAndHeal.
func (t *MLTalker) Watch() {}

func clamp(val float64, min float64, max float64) float64 {
	if val < min {
		return min
	}
	if val > max {
		return max
	}
	return val
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func bytesToMiB(v float64) float64 {
	const mib = 1024.0 * 1024.0
	if v <= 0 {
		return 0
	}
	return v / mib
}

func (t *MLTalker) shouldSkipAction(actionKey string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if until, ok := t.cooldowns[actionKey]; ok && time.Now().Before(until) {
		return true
	}
	t.cooldowns[actionKey] = time.Now().Add(t.cooldownFor)
	return false
}

func (t *MLTalker) callPredictEndpoint(dp *UnifiedDataPoint, pod *v1.Pod) (*MLPredictResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := url.Values{}

	// Prometheus reports memory in bytes, but training data is in MiB-like scale.
	memUsageMiB := bytesToMiB(dp.MemoryUsage)
	memLimitMiB := bytesToMiB(dp.MemoryLimit)

	cpuRatio := 0.0
	if dp.CPULimit > 0 {
		cpuRatio = dp.CPUUsage / dp.CPULimit
	}
	memRatio := 0.0
	if memLimitMiB > 0 {
		memRatio = memUsageMiB / memLimitMiB
	}

	// Allocation efficiency proxies for runtime inference.
	cpuEfficiency := clamp(1.0-abs(1.0-cpuRatio), 0.0, 1.0)
	memEfficiency := clamp(1.0-abs(1.0-memRatio), 0.0, 1.0)

	cpuRequest, memRequestBytes := t.getPodRequests(pod)
	memRequestMiB := bytesToMiB(memRequestBytes)
	deploymentStrategy := t.getDeploymentStrategyForPod(ctx, pod)
	scalingPolicy := t.getScalingPolicyForPod(ctx, pod)

	networkLatency := dp.P95ResponseTime
	if networkLatency == 0 {
		networkLatency = dp.AvgResponseTime
	}

	q.Set("cpu_allocation_efficiency", strconv.FormatFloat(cpuEfficiency, 'f', 6, 64))
	q.Set("memory_allocation_efficiency", strconv.FormatFloat(memEfficiency, 'f', 6, 64))
	q.Set("disk_io", strconv.FormatFloat(float64(dp.TotalRequests), 'f', 2, 64))
	q.Set("network_latency", strconv.FormatFloat(networkLatency, 'f', 2, 64))
	q.Set("node_temperature", "65")
	q.Set("node_cpu_usage", strconv.FormatFloat(clamp(dp.CPUPercent, 0, 100), 'f', 2, 64))
	q.Set("node_memory_usage", strconv.FormatFloat(clamp(dp.MemoryPercent, 0, 100), 'f', 2, 64))
	q.Set("pod_lifetime_seconds", strconv.FormatFloat(dp.PodAge*3600, 'f', 0, 64))
	q.Set("scaling_event", "false")
	q.Set("cpu_request", strconv.FormatFloat(cpuRequest, 'f', 6, 64))
	q.Set("cpu_limit", strconv.FormatFloat(dp.CPULimit, 'f', 6, 64))
	q.Set("memory_request", strconv.FormatFloat(memRequestMiB, 'f', 2, 64))
	q.Set("memory_limit", strconv.FormatFloat(memLimitMiB, 'f', 2, 64))
	q.Set("cpu_usage", strconv.FormatFloat(dp.CPUUsage, 'f', 6, 64))
	q.Set("memory_usage", strconv.FormatFloat(memUsageMiB, 'f', 2, 64))
	q.Set("restart_count", strconv.Itoa(int(dp.RestartCount)))
	q.Set("uptime_seconds", strconv.FormatFloat(dp.PodAge*3600, 'f', 0, 64))
	q.Set("network_bandwidth_usage", strconv.FormatFloat(dp.RequestRate, 'f', 2, 64))
	q.Set("namespace", pod.Namespace)
	q.Set("deployment_strategy", deploymentStrategy)
	q.Set("scaling_policy", scalingPolicy)

	endpoint := fmt.Sprintf("%s/predict?%s", t.apiURL, q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ml api status %d", resp.StatusCode)
	}

	result := &MLPredictResponse{}
	if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
		return nil, err
	}
	return result, nil
}

func (t *MLTalker) getPodRequests(pod *v1.Pod) (cpu float64, mem float64) {
	if len(pod.Spec.Containers) == 0 {
		return 0, 0
	}
	c := pod.Spec.Containers[0]
	if qty, ok := c.Resources.Requests[v1.ResourceCPU]; ok {
		cpu = qty.AsApproximateFloat64()
	}
	if qty, ok := c.Resources.Requests[v1.ResourceMemory]; ok {
		mem = qty.AsApproximateFloat64()
	}
	return cpu, mem
}

func (t *MLTalker) resolveDeploymentForPod(ctx context.Context, pod *v1.Pod) (string, error) {
	for _, owner := range pod.OwnerReferences {
		if owner.Kind == "Deployment" {
			return owner.Name, nil
		}
		if owner.Kind == "ReplicaSet" {
			rs, err := t.k8sClient.AppsV1().ReplicaSets(pod.Namespace).Get(ctx, owner.Name, metav1.GetOptions{})
			if err != nil {
				return "", err
			}
			for _, rsOwner := range rs.OwnerReferences {
				if rsOwner.Kind == "Deployment" {
					return rsOwner.Name, nil
				}
			}
		}
	}
	return "", fmt.Errorf("deployment owner not found for pod %s", pod.Name)
}

func (t *MLTalker) getDeploymentStrategyForPod(ctx context.Context, pod *v1.Pod) string {
	depName, err := t.resolveDeploymentForPod(ctx, pod)
	if err != nil {
		return "RollingUpdate"
	}
	dep, err := t.k8sClient.AppsV1().Deployments(pod.Namespace).Get(ctx, depName, metav1.GetOptions{})
	if err != nil {
		return "RollingUpdate"
	}
	if dep.Spec.Strategy.Type == "Recreate" {
		return "Recreate"
	}
	return "RollingUpdate"
}

func (t *MLTalker) getScalingPolicyForPod(ctx context.Context, pod *v1.Pod) string {
	depName, err := t.resolveDeploymentForPod(ctx, pod)
	if err != nil {
		return "Manual"
	}
	hpas, err := t.k8sClient.AutoscalingV2().HorizontalPodAutoscalers(pod.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return "Manual"
	}
	for _, hpa := range hpas.Items {
		if hpa.Spec.ScaleTargetRef.Kind == "Deployment" && hpa.Spec.ScaleTargetRef.Name == depName {
			return "Auto"
		}
	}
	return "Manual"
}

func (t *MLTalker) restartPod(ctx context.Context, pod *v1.Pod) error {
	fmt.Printf("[self-heal] restarting pod %s/%s\n", pod.Namespace, pod.Name)
	return t.k8sClient.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})
}

func (t *MLTalker) scaleUpDeployment(ctx context.Context, pod *v1.Pod) error {
	depName, err := t.resolveDeploymentForPod(ctx, pod)
	if err != nil {
		return err
	}

	depClient := t.k8sClient.AppsV1().Deployments(pod.Namespace)
	dep, err := depClient.Get(ctx, depName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	cur := int32(1)
	if dep.Spec.Replicas != nil {
		cur = *dep.Spec.Replicas
	}
	if cur >= 10 {
		fmt.Printf("[self-heal] scale capped for %s/%s at %d replicas\n", pod.Namespace, depName, cur)
		return nil
	}

	next := cur + 1
	dep.Spec.Replicas = &next
	_, err = depClient.Update(ctx, dep, metav1.UpdateOptions{})
	if err != nil {
		return err
	}

	fmt.Printf("[self-heal] scaled deployment %s/%s from %d to %d\n", pod.Namespace, depName, cur, next)
	return nil
}

func (t *MLTalker) applyAction(ctx context.Context, action string, pod *v1.Pod) error {
	if action == "none" || action == "investigate" {
		fmt.Printf("[self-heal] pod %s/%s action=%s (no mutating op)\n", pod.Namespace, pod.Name, action)
		return nil
	}

	actionKey := fmt.Sprintf("%s:%s:%s", pod.Namespace, pod.Name, action)
	if t.shouldSkipAction(actionKey) {
		fmt.Printf("[self-heal] skipping %s for %s/%s due to cooldown\n", action, pod.Namespace, pod.Name)
		return nil
	}

	switch action {
	case "restart_pod":
		return t.restartPod(ctx, pod)
	case "scale_up":
		return t.scaleUpDeployment(ctx, pod)
	default:
		fmt.Printf("[self-heal] unsupported action '%s' for %s/%s\n", action, pod.Namespace, pod.Name)
		return nil
	}
}

// EvaluateAndHeal sends per-pod features to FastAPI and applies remediation actions.
func (t *MLTalker) EvaluateAndHeal(dataPoints []*UnifiedDataPoint) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pods, err := t.k8sClient.CoreV1().Pods("default").List(ctx, metav1.ListOptions{})
	if err != nil {
		fmt.Printf("[self-heal] failed to list pods: %v\n", err)
		return
	}

	podByName := make(map[string]*v1.Pod)
	for i := range pods.Items {
		pod := &pods.Items[i]
		podByName[pod.Name] = pod
	}

	for _, dp := range dataPoints {
		pod, ok := podByName[dp.PodName]
		if !ok {
			continue
		}

		pred, err := t.callPredictEndpoint(dp, pod)
		if err != nil {
			fmt.Printf("[self-heal] ml predict failed for pod %s: %v\n", dp.PodName, err)
			continue
		}

		fmt.Printf("[self-heal] pod=%s health=%s(%.2f) action=%s final=%s\n",
			dp.PodName,
			pred.HealthState,
			pred.HealthConfidence,
			pred.ActionModelDecision,
			pred.FinalRecommendedAction,
		)

		actionCtx, cancelAction := context.WithTimeout(context.Background(), 30*time.Second)
		err = t.applyAction(actionCtx, pred.FinalRecommendedAction, pod)
		cancelAction()
		if err != nil {
			fmt.Printf("[self-heal] action failed for pod %s: %v\n", dp.PodName, err)
		}
	}
}
