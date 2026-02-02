package internals

import (
	"encoding/json"
	"fmt"
	"net/http"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/client-go/kubernetes"
)

type K8sService struct {
	k8sClient *kubernetes.Clientset
}

type ErrorMessage struct {
	Message string `json:"message"`
}

type NodeInfo struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}
type GetNodesResponseDto struct {
	Nodes []NodeInfo `json:"nodes"`
}

type PodInfo struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Status    string `json:"status"`
}
type GetPodsResponseDto struct {
	Pods []PodInfo `json:"pods"`
}

func NewK8sService(k8sClient *kubernetes.Clientset) *K8sService {
	return &K8sService{
		k8sClient,
	}
}

func (s *K8sService) GetNodes(w http.ResponseWriter, r *http.Request) {
	encoder := json.NewEncoder(w)
	response := GetNodesResponseDto{}
	encoder.Encode(response)
	w.WriteHeader(http.StatusAccepted)
	w.Header().Set("Content-Type", "application/json")
}

func (s *K8sService) GetPods(w http.ResponseWriter, r *http.Request) {
	encoder := json.NewEncoder(w)
	w.Header().Set("Content-Type", "application/json")
	ctx := r.Context()

	namespace := "monitoring"
	pods, err := s.k8sClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		response := ErrorMessage{Message: "Failed to get pods"}
		encoder.Encode(response)
		return
	}
	podsInfo := []PodInfo{}
	for _, pod := range pods.Items {
		podsInfo = append(podsInfo, PodInfo{Name: pod.Name, Status: string(pod.Status.Phase), Namespace: pod.Namespace})
		fmt.Printf("Pod: %s, Status: %s\n", pod.Name, pod.Status.Phase)
	}
	response := GetPodsResponseDto{Pods: podsInfo}
	w.WriteHeader(http.StatusAccepted)
	encoder.Encode(response)
}
