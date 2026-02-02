package pkg

import (
	"context"
	"fmt"
	"path/filepath"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

type K8sClient struct {
	Client *kubernetes.Clientset
}

func NewK8sClient() (*kubernetes.Clientset, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		kubeconfig := filepath.Join(homedir.HomeDir(), ".kube", "config")
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, err
		}
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return clientset, err
}

// func NewK8sClient() (*K8sClient, error) {
// 	config, err := rest.InClusterConfig()
// 	if err != nil {
// 		kubeconfig := filepath.Join(homedir.HomeDir(), ".kube", "config")
// 		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
// 		if err != nil {
// 			return nil, err
// 		}
// 	}
// 	clientset, err := kubernetes.NewForConfig(config)
// 	if err != nil {
// 		return nil, err
// 	}
// 	return &K8sClient{Client: clientset}, err
// }

func (k *K8sClient) ListPods(ctx context.Context, namespace string) error {
	pods, err := k.Client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, pod := range pods.Items {
		fmt.Printf("Pod: %s, Status: %s\n", pod.Name, pod.Status.Phase)
	}
	return nil
}
