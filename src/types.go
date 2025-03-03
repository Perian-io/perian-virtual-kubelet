package perian

import (
	perian_client "github.com/Perian-io/perian-go-client"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

type Config struct {
	PerianServerURL string `yaml:"PerianServerURL"`
	PerianOrg       string `yaml:"PerianOrg"`
	PerianAuthToken string `yaml:"PerianAuthToken"`
	KubeletPort     int32  `yaml:"KubeletPort"`
	NodeName        string `yaml:"NodeName"`
}

type Provider struct {
	operatingSystem      string
	jobClient            *perian_client.JobAPIService
	node                 *corev1.Node
	onNodeChangeCallback func(*corev1.Node)
	internalIP           string
	kubeletPort          int32
	notifier             func(*corev1.Pod)
	perianPods           map[string]PerianPod
	clientSet            *kubernetes.Clientset
}

type PerianPod struct {
	jobId string
	pod   *corev1.Pod
}
