package perian

import (
	perian_client "github.com/Perian-io/perian-go-client"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

// Config represents the Perian Virtual Kubelet configurations.
type Config struct {
	PerianServerURL string `yaml:"PerianServerURL"`
	PerianOrg       string `yaml:"PerianOrg"`
	PerianAuthToken string `yaml:"PerianAuthToken"`
	KubeletPort     int32  `yaml:"KubeletPort"`
	NodeName        string `yaml:"NodeName"`
	InternalIP      string `yaml:"InternalIP"`
}

// Provider represents the Virtual Kubelet (VK) provider struct which holds the data necessary for running the VK
// operations (CreatePod, UpdatePod, ...).
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

// PerianPod represents a pod and its jobId which runs on the Perian sky platform.
type PerianPod struct {
	jobId string
	pod   *corev1.Pod
}

// DockerSecret represents the docker registry credentials and url for authentication.
type DockerSecret struct {
	registryURL string
	username    string
	password    string
}

// JobResources represents the requested resources to run on the Perian sky platform.
type JobResources struct {
	cpu             int32
	memory          string
	accelerators    int32
	acceleratorType string
}
