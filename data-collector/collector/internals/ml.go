package internals

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/robfig/cron/v3"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type MLTalker struct {
	k8sClient *kubernetes.Clientset
	cron      *cron.Cron
}

func NewMLTalker(k8sClient *kubernetes.Clientset) *MLTalker {
	cron := cron.New(cron.WithSeconds())
	return &MLTalker{
		k8sClient,
		cron,
	}
}

func (this *MLTalker) Watch() {
	// this.watchLogs()
	this.cron.Start()
}

func (this *MLTalker) watchLogs() {
	_, err := this.cron.AddFunc("@every 10s", func() {
		logsFrom := metav1.NewTime(time.Now().Add(-10 * time.Second))
		println(logsFrom.String())
		ctx := context.Background()
		pods, err := this.k8sClient.CoreV1().Pods("default").List(ctx, metav1.ListOptions{})
		if err != nil {
			fmt.Printf("Error fetching pods: %v\n", err)
			return
		}
		// for _, pod := range pods.Items {
		// 	fmt.Printf("%s\n", pod.Name)
		// }

		req := this.k8sClient.CoreV1().Pods("default").GetLogs(pods.Items[0].Name, &v1.PodLogOptions{
			SinceTime: &logsFrom,
		})
		podLogs, err := req.Stream(ctx)
		if err != nil {
			fmt.Printf("Error opening stream: %v\n", err)
			return
		}
		defer podLogs.Close()

		buf := new(bytes.Buffer)
		_, err = io.Copy(buf, podLogs)
		if err != nil {
			fmt.Printf("Error copying logs: %v\n", err)
			return
		}

		fmt.Println(buf.String())
	})
	if err != nil {
		fmt.Printf("Failed to add a cronjob: %v\n", err)
		return
	}
}
