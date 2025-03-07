package perian

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/Perian-io/perian-go-client"
	"github.com/virtual-kubelet/virtual-kubelet/node/api"
	stats "github.com/virtual-kubelet/virtual-kubelet/node/api/statsv1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

func NewProvider(
	nodeName string,
	operatingSystem string,
	url string,
	organization string,
	token string,
	kubeletPort int32,
	kubeClient *kubernetes.Clientset,
	internalIP string,
) (*Provider, error) {

	labels := GetProviderLabels(nodeName)
	jobClient := InitPerianJobClient(url, token, organization)
	taints := GetNodeTaints()
	node := CreateKubeNode(nodeName, labels, taints, internalIP, kubeletPort)
	provider := InitProvider(jobClient, operatingSystem, node, kubeletPort, internalIP, kubeClient)
	return &provider, nil
}

func (p *Provider) CreatePod(ctx context.Context, pod *corev1.Pod) error {
	imageName := GetPodImageName(pod)
	fullName := GeneratePodKey(pod)
	dockerSecret, err := GetDockerSecrets(ctx, p.clientSet)
	if err != nil {
		pod.Status = PodPhase(perian.JOBSTATUS_SERVER_ERROR, p.internalIP)
		return err
	}
	jobResources := GetJobResources(pod)
	jobId, err := CreatePerianJob(ctx, p.jobClient, imageName, *dockerSecret, jobResources)
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

func (p *Provider) UpdatePod(ctx context.Context, pod *corev1.Pod) error {
	p.notifier(pod)
	return nil
}

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

func (p *Provider) GetPodStatus(ctx context.Context, namespace, name string) (*corev1.PodStatus, error) {
	pod, err := p.GetPod(ctx, namespace, name)
	if err != nil {
		return nil, err
	}
	return &pod.Status, nil
}

func (p *Provider) GetPod(ctx context.Context, namespace, name string) (*corev1.Pod, error) {
	fullName := CreateKeyFromNamespaceAndName(namespace, name)
	perianPod, ok := p.perianPods[fullName]
	if !ok {
		return nil, fmt.Errorf("pod %s is not found", fullName)
	}
	pod := perianPod.pod
	return pod, nil
}

func (p *Provider) GetPods(ctx context.Context) ([]*corev1.Pod, error) {
	err := p.GetNodePodsFromCluster(ctx)
	if err != nil {
		return nil, err
	}
	pods := GetProviderPodList(p)
	return pods, nil
}

func (p *Provider) GetNode() *corev1.Node {
	return p.node
}

func (p *Provider) NotifyNodeStatus(ctx context.Context, f func(*corev1.Node)) {
	p.onNodeChangeCallback = f
	go p.nodeUpdate(ctx)
}

func (p *Provider) Ping(ctx context.Context) error {
	return nil
}

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

func (p *Provider) GetStatsSummary(ctx context.Context) (*stats.Summary, error) {
	return nil, nil
}

func (p *Provider) NotifyPods(ctx context.Context, f func(*corev1.Pod)) {
	p.notifier = f
	go p.statusLoop(ctx)
}

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

func (p *Provider) GetNodePodsFromCluster(ctx context.Context) error {
	namespaces, err := GetClusterNamespaces(ctx, p.clientSet)
	if err != nil {
		return err
	}

	for _, ns := range namespaces.Items {
		podsList, err := GetPodsList(ctx, p.clientSet, ns.Name)
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

func (p *Provider) AddPerianPod(pod *corev1.Pod) {
	fullName := GeneratePodKey(pod)
	p.perianPods[fullName] = PerianPod{
		jobId: pod.Annotations["jobId"],
		pod:   pod,
	}
}
