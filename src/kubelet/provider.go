// Package perian provides the functionality required to run a VK node on your Kubernetes cluster to extend it using
// our Perian sky platform.
package perian

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/Perian-io/perian-go-client"
	"github.com/virtual-kubelet/virtual-kubelet/node/api"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

// NewProvider creates and returns a new provider object by initializing it with the required values.
//
// It creates a new virtual node in the Kubernetes cluster which represents the node which VK runs on.
func NewProvider(
	nodeName string,
	operatingSystem string,
	url string,
	organization string,
	token string,
	kubeletPort int32,
	kubeClient *kubernetes.Clientset,
	internalIP string,
	kubernetesSecretName string,
) (*Provider, error) {

	labels := GetProviderNodeLabels(nodeName)
	jobClient := InitPerianJobClient(url, token, organization)
	taints := GetNodeTaints()
	node := CreateKubeNode(nodeName, labels, taints, internalIP, kubeletPort)
	provider := InitProvider(jobClient, operatingSystem, node, kubeletPort, internalIP, kubeClient, kubernetesSecretName)
	return &provider, nil
}

// CreatePod takes a Kubernetes Pod and creates it on the Perian sky platform.
//
// It extracts the required specs, docker registry credentials, and the container to run and sends it to the Perian sky
// platform to run as a job.
func (p *Provider) CreatePod(ctx context.Context, pod *corev1.Pod) error {
	imageName := GetPodImageName(pod)
	fullName := GeneratePodKey(pod)
	dockerSecret, _ := GetDockerSecrets(ctx, p.clientSet, p.kubernetesSecretName)
	jobResources := GetJobResources(pod)
	jobId, err := CreatePerianJob(ctx, p.jobClient, imageName, dockerSecret, jobResources)
	if err != nil {
		pod.Status = PodPhase(perian.JOBSTATUS_SERVER_ERROR, p.internalIP)
		return err
	}
	AnnotatePodWithJobID(pod, jobId)
	p.perianPods[fullName] = PerianPod{
		jobId: jobId,
		pod:   pod,
	}
	SetPodStatusToPending(pod, p.internalIP)
	SetPodContainerStatusToInit(pod)
	return err
}

// UpdatePod takes a Kubernetes Pod and updates it on the Perian sky platform.
func (p *Provider) UpdatePod(ctx context.Context, pod *corev1.Pod) error {
	p.notifier(pod)
	return nil
}

// DeletePod takes a Kubernetes Pod and deletes it on the Perian sky platform.
//
// It sends a request to the Perian sky platform to cancel the equivalent running job.
func (p *Provider) DeletePod(ctx context.Context, pod *corev1.Pod) error {
	fullName := GeneratePodKey(pod)
	perianJobId, err := GetPodJobId(p, fullName)
	if err != nil {
		return err
	}
	err = CancelPerianJobById(ctx, *p.jobClient, perianJobId)
	if err != nil {
		return err
	}
	UpdateDeletedPodStatus(pod)
	delete(p.perianPods, fullName)
	_ = p.UpdatePod(ctx, pod)
	return nil
}

// GetPodStatus retrieves the status of a pod on the Perian sky platform by name.
//
// It sends a Get request to the Perian sky platform to check the job status and maos it to a Kubernetes Pod status.
func (p *Provider) GetPodStatus(ctx context.Context, namespace, name string) (*corev1.PodStatus, error) {
	pod, err := p.GetPod(ctx, namespace, name)
	if err != nil {
		return nil, err
	}
	return &pod.Status, nil
}

// GetPod retrieves a Pod by name from the Perian sky platform.
func (p *Provider) GetPod(ctx context.Context, namespace, name string) (*corev1.Pod, error) {
	fullName := CreateKeyFromNamespaceAndName(namespace, name)
	perianPod, ok := p.perianPods[fullName]
	if !ok {
		return nil, fmt.Errorf("pod %s is not found", fullName)
	}
	pod := perianPod.pod
	return pod, nil
}

// GetPods retrieves a list of all Pods running on the Perian sky platform.
func (p *Provider) GetPods(ctx context.Context) ([]*corev1.Pod, error) {
	err := p.GetNodePodsFromCluster(ctx)
	if err != nil {
		return nil, err
	}
	pods := GetProviderPodList(p)
	return pods, nil
}

// GetNode retrieves the virtual Kubernetes node which represents the server where VK runs.
func (p *Provider) GetNode() *corev1.Node {
	return p.node
}

// NotifyNodeStatus updates the virtual Kubernetes node status.
//
// It creates a goroutine which runs forever to keep updating the node status.
func (p *Provider) NotifyNodeStatus(ctx context.Context, f func(*corev1.Node)) {
	p.onNodeChangeCallback = f
	go p.nodeUpdate(ctx)
}

// Ping pings the Kubernetes node.
func (p *Provider) Ping(ctx context.Context) error {
	return nil
}

// nodeUpdate runs a loop which updates the Kubernetes node status every 30 seconds.
func (p *Provider) nodeUpdate(ctx context.Context) {
	t := time.NewTimer(5 * time.Second)
	if !t.Stop() {
		<-t.C
	}
	for {
		t.Reset(30 * time.Second)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		p.node.Status.Conditions = GetNodeCondition(true)
		p.onNodeChangeCallback(p.node)
	}
}

// GetLogs retrieves a Pod container logs from the Perian sky platform.
//
// It takes a pod name and a container name and sends a Get request to the Perian sky platform to get the logs from the
// running job.
func (p *Provider) GetLogs(ctx context.Context, namespace string, podName string, containerName string, opts api.ContainerLogOpts) (io.ReadCloser, error) {
	fullName := CreateKeyFromNamespaceAndName(namespace, podName)
	jobId, err := GetPodJobId(p, fullName)
	if err != nil {
		return nil, err
	}
	logs, err := GetPerianJobLogs(ctx, *p.jobClient, jobId)
	if err != nil {
		return nil, err
	}
	return stringToReadCloser(logs), nil
}

// NotifyPods creates a goroutine which runs forever to keep checking and updating Pods statuses running on the Perian
// sky platform.
func (p *Provider) NotifyPods(ctx context.Context, f func(*corev1.Pod)) {
	p.notifier = f
	go p.statusLoop(ctx)
}

// statusLoop runs a loop to fetch the status of a job and update the Pod status every 30 seconds.
func (p *Provider) statusLoop(ctx context.Context) {
	t := time.NewTimer(30 * time.Second)
	if !t.Stop() {
		<-t.C
	}

	for {
		t.Reset(30 * time.Second)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}

		for _, perianPod := range p.perianPods {
			jobId := perianPod.jobId
			pod := perianPod.pod
			if !IsPodSucceeded(pod) {
				job, err := GetPerianJobById(ctx, *p.jobClient, jobId)
				if err != nil {
				}
				jobStatus := GetJobStatus(*job)
				SetPodStatus(pod, p.internalIP, jobStatus, job.CreatedAt, job.DoneAt.Get())
				p.UpdatePod(ctx, pod)
			}
		}
	}
}

// GetNodePodsFromCluster fetches all the running Pods in a cluster which should be running on the VK node.
func (p *Provider) GetNodePodsFromCluster(ctx context.Context) error {
	namespaces, err := GetClusterNamespaces(ctx, p.clientSet)
	if err != nil {
		return err
	}

	for _, ns := range namespaces.Items {
		podsList, err := GetNamespacePodsList(ctx, p.clientSet, ns.Name)
		if err != nil {
			return err
		}
		for _, pod := range podsList.Items {

			if CheckAnnotatedPod(&pod, "jobId") && IsPodRunningOnNode(&pod, p.node) {
				p.AddPerianPod(&pod)
				p.notifier(&pod)
			}
		}
	}
	return nil
}

// AddPerianPod adds a Pod to the map of Pods running on the Perian sky platform.
func (p *Provider) AddPerianPod(pod *corev1.Pod) {
	fullName := GeneratePodKey(pod)
	p.perianPods[fullName] = PerianPod{
		jobId: pod.Annotations["jobId"],
		pod:   pod,
	}
}
