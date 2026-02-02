package internals

import (
	"net/http"

	"k8s.io/client-go/kubernetes"
)

type K8sRouter struct {
	K8sClient *kubernetes.Clientset
}

func NewK8sRouter(k8sClient *kubernetes.Clientset) http.Handler {
	k8sService := NewK8sService(k8sClient)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /nodes", k8sService.GetNodes)
	mux.HandleFunc("GET /pods", k8sService.GetPods)

	return mux
}
