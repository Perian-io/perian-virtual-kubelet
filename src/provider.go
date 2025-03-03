package perian

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	perian_client "github.com/Perian-io/perian-go-client"
	"github.com/virtual-kubelet/virtual-kubelet/node/api"
	stats "github.com/virtual-kubelet/virtual-kubelet/node/api/statsv1alpha1"
	"google.golang.org/appengine/log"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func NewProvider(
	nodeName string,
	operatingSystem string,
	url string,
	org string,
	token string,
	kubeletPort int32,
) (*Provider, error) {
	lbls := map[string]string{
		"alpha.service-controller.kubernetes.io/exclude-balancer": "true",
		"beta.kubernetes.io/os":                                   "linux",
		"kubernetes.io/os":                                        "linux",
		"kubernetes.io/hostname":                                  nodeName,
		"kubernetes.io/role":                                      "agent",
		"node.kubernetes.io/exclude-from-external-load-balancers": "true",
		"virtual-node.perian/type":                                "virtual-kubelet",
	}

	cfg := perian_client.NewConfiguration()
	cfg.Servers = perian_client.ServerConfigurations{
		{
			URL: url,
		},
	}
	cfg.AddDefaultHeader("Authorization", token)
	cfg.AddDefaultHeader("X-PERIAN-AUTH-ORG", org)
	apiClient := perian_client.NewAPIClient(cfg)

	jobClient := apiClient.JobAPI

	taints := []corev1.Taint{
		{
			Key:    "virtual-node.perian/no-schedule",
			Value:  strconv.FormatBool(false),
			Effect: corev1.TaintEffectNoSchedule,
		},
	}

	node := corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   nodeName,
			Labels: lbls,
		},
		Spec: corev1.NodeSpec{
			ProviderID: "external:///" + nodeName,
			Taints:     taints,
		},
		Status: corev1.NodeStatus{
			NodeInfo: corev1.NodeSystemInfo{
				KubeletVersion:  "v1.32.0",
				Architecture:    "virtual-kubelet",
				OperatingSystem: "linux",
			},
			Addresses:       []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "127.0.0.1"}},
			DaemonEndpoints: corev1.NodeDaemonEndpoints{KubeletEndpoint: corev1.DaemonEndpoint{Port: kubeletPort}},
			Conditions:      NodeCondition(false),
			Capacity:        GetResources(),
			Allocatable:     GetResources(),
		},
	}

	provider := Provider{
		jobClient:       jobClient,
		operatingSystem: operatingSystem,
		node:            &node,
		kubeletPort:     kubeletPort,
		internalIP:      "127.0.0.1",
	}
	provider.perianPods = make(map[string]PerianPod)
	return &provider, nil
}

func (p *Provider) CreatePod(ctx context.Context, pod *corev1.Pod) error {
	specs := pod.Spec
	container := specs.Containers[0]
	imageName := container.Image
	containerName := container.Name
	fullName := pod.Namespace + "/" + containerName

	createJobAPI := p.jobClient.CreateJob(ctx)
	createJobRequest := perian_client.NewCreateJobRequest()

	dockerRunParameters := perian_client.NewDockerRunParameters()
	dockerRunParameters.SetImageName(imageName)
	requirements := perian_client.NewInstanceTypeQueryInput()
	createJobRequest.SetRequirements(*requirements)
	createJobRequest.SetDockerRunParameters(*dockerRunParameters)
	createJobAPI = createJobAPI.CreateJobRequest(*createJobRequest)

	createJobSuccess, _, err := createJobAPI.Execute()

	if err != nil {
		_ = fmt.Errorf(err.Error())
		pod.Status, _ = PodPhase(*p, "Failed")
		return err
	}

	jobId := createJobSuccess.GetId()
	pod.Annotations["jobId"] = jobId
	p.perianPods[fullName] = PerianPod{
		jobId: jobId,
		pod:   pod,
	}

	pod.Status, err = PodPhase(*p, "Pending")

	if err != nil {
		_ = fmt.Errorf(err.Error())
		pod.Status, _ = PodPhase(*p, "Failed")
		return err
	}

	pod.Status.ContainerStatuses = []corev1.ContainerStatus{
		{
			Name:         container.Name,
			Image:        container.Image,
			Ready:        false,
			RestartCount: 0,
			State: corev1.ContainerState{
				Waiting: &corev1.ContainerStateWaiting{
					Reason: "Waiting for InitContainers",
				},
			},
		},
	}
	return err
}

func (p *Provider) UpdatePod(ctx context.Context, pod *corev1.Pod) error {
	p.notifier(pod)
	return nil
}

func (p *Provider) DeletePod(ctx context.Context, pod *corev1.Pod) error {
	fullName := pod.Namespace + "/" + pod.Spec.Containers[0].Name
	perianJobId := p.perianPods[fullName].jobId
	cancelJobAPI := p.jobClient.CancelJob(ctx, perianJobId)
	response, _, err := cancelJobAPI.Execute()
	if *response.StatusCode != 200 || err != nil {
		fmt.Errorf("couldn't cancel job id %s", perianJobId)
	}
	pod.Status.ContainerStatuses[0].Ready = false
	pod.Status.ContainerStatuses[0].State = corev1.ContainerState{
		Terminated: &corev1.ContainerStateTerminated{
			FinishedAt: metav1.Now(),
			Message:    "perian job deleted",
		},
	}
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
	fullName := namespace + "/" + name
	perianPod, ok := p.perianPods[fullName]
	if !ok {
		return nil, fmt.Errorf("pod %s is not found", fullName)
	}
	pod := perianPod.pod
	return pod, nil
}

func (p *Provider) GetPods(ctx context.Context) ([]*corev1.Pod, error) {
	err := p.InitClientSet(ctx)
	if err != nil {
		return nil, err
	}

	err = p.RetrievePodsFromCluster(ctx)
	if err != nil {
		return nil, err
	}

	var pods []*corev1.Pod
	for _, perianPod := range p.perianPods {
		pods = append(pods, perianPod.pod)
	}

	return pods, nil
}

func (p *Provider) GetNode() *corev1.Node {
	return p.node
}

func (p *Provider) NotifyNodeStatus(ctx context.Context, f func(*corev1.Node)) {
	p.onNodeChangeCallback = f
	go p.nodeUpdate()
}

func (p *Provider) Ping(ctx context.Context) error {
	return nil
}

func (p *Provider) nodeUpdate() {
	p.node.Status.Conditions = NodeCondition(true)
	p.onNodeChangeCallback(p.node)
}

func (p *Provider) GetLogs(ctx context.Context, namespace, podName, containerName string, opts api.ContainerLogOpts) (io.ReadCloser, error) {
	fullName := namespace + "/" + podName
	jobId := p.perianPods[fullName].jobId
	jobResponse, _, err := p.jobClient.GetJobById(ctx, jobId).Execute()
	if *jobResponse.StatusCode != 200 || err != nil {
		return nil, fmt.Errorf("couldn't get logs for job id %s", jobId)
	}
	logs := *jobResponse.Jobs[0].Logs.Get()
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

		var jobId string
		var pod *corev1.Pod

		for _, perianPod := range p.perianPods {
			jobId = perianPod.jobId
			pod = perianPod.pod
			p.UpdatePod(ctx, pod)
			jobResponse, _, err := p.jobClient.GetJobById(ctx, jobId).Execute()
			if *jobResponse.StatusCode != 200 || err != nil {
				log.Errorf(ctx, "couldn't get job id %s", jobId)
			}
			jobStatus := jobResponse.Jobs[0].Status
			if *jobStatus == perian_client.JOBSTATUS_CANCELLED {
				pod.Status, err = PodPhase(*p, "Failed")
				pod.Status.ContainerStatuses = []corev1.ContainerStatus{
					{
						Name:         pod.Spec.Containers[0].Name,
						Image:        pod.Spec.Containers[0].Image,
						Ready:        false,
						RestartCount: 0,
						State: corev1.ContainerState{
							Terminated: &corev1.ContainerStateTerminated{
								Reason: "Job is cancelled",
							},
						},
					},
				}

			} else if *jobStatus == perian_client.JOBSTATUS_SERVER_ERROR || *jobStatus == perian_client.JOBSTATUS_USER_ERROR {
				pod.Status, err = PodPhase(*p, "Failed")
				pod.Status.ContainerStatuses = []corev1.ContainerStatus{
					{
						Name:         pod.Spec.Containers[0].Name,
						Image:        pod.Spec.Containers[0].Image,
						Ready:        false,
						RestartCount: 0,
						State: corev1.ContainerState{
							Terminated: &corev1.ContainerStateTerminated{
								Reason: "Job resulted in an error",
							},
						},
					},
				}
			} else if *jobStatus == perian_client.JOBSTATUS_INITIALIZING || *jobStatus == perian_client.JOBSTATUS_QUEUED {
				pod.Status, err = PodPhase(*p, "Pending")
				pod.Status.ContainerStatuses = []corev1.ContainerStatus{
					{
						Name:         pod.Spec.Containers[0].Name,
						Image:        pod.Spec.Containers[0].Image,
						Ready:        false,
						RestartCount: 0,
						State: corev1.ContainerState{
							Waiting: &corev1.ContainerStateWaiting{
								Reason: "Job is initializing",
							},
						},
					},
				}
			} else if *jobStatus == perian_client.JOBSTATUS_RUNNING {
				pod.Status, err = PodPhase(*p, "Running")
				pod.Status.ContainerStatuses = []corev1.ContainerStatus{
					{
						Name:         pod.Spec.Containers[0].Name,
						Image:        pod.Spec.Containers[0].Image,
						Ready:        false,
						RestartCount: 0,
						State: corev1.ContainerState{
							Running: &corev1.ContainerStateRunning{
								StartedAt: metav1.Now(),
							},
						},
					},
				}
			} else if *jobStatus == perian_client.JOBSTATUS_DONE {
				pod.Status, err = PodPhase(*p, "Running")
				pod.Status.ContainerStatuses = []corev1.ContainerStatus{
					{
						Name:         pod.Spec.Containers[0].Name,
						Image:        pod.Spec.Containers[0].Image,
						Ready:        true,
						RestartCount: 0,
						State: corev1.ContainerState{
							Terminated: &corev1.ContainerStateTerminated{
								StartedAt:  metav1.Time{*jobResponse.Jobs[0].CreatedAt},
								FinishedAt: metav1.Now(),
								ExitCode:   0,
							},
						},
					},
				}
			}
		}
	}
}

func NodeCondition(ready bool) []corev1.NodeCondition {
	var readyType corev1.ConditionStatus
	var netType corev1.ConditionStatus
	if ready {
		readyType = corev1.ConditionTrue
		netType = corev1.ConditionFalse
	} else {
		readyType = corev1.ConditionFalse
		netType = corev1.ConditionTrue
	}

	return []corev1.NodeCondition{
		{
			Type:               "Ready",
			Status:             readyType,
			LastHeartbeatTime:  metav1.Now(),
			LastTransitionTime: metav1.Now(),
			Reason:             "KubeletPending",
			Message:            "kubelet is pending.",
		},
		{
			Type:               "OutOfDisk",
			Status:             corev1.ConditionFalse,
			LastHeartbeatTime:  metav1.Now(),
			LastTransitionTime: metav1.Now(),
			Reason:             "KubeletHasSufficientDisk",
			Message:            "kubelet has sufficient disk space available",
		},
		{
			Type:               "MemoryPressure",
			Status:             corev1.ConditionFalse,
			LastHeartbeatTime:  metav1.Now(),
			LastTransitionTime: metav1.Now(),
			Reason:             "KubeletHasSufficientMemory",
			Message:            "kubelet has sufficient memory available",
		},
		{
			Type:               "DiskPressure",
			Status:             corev1.ConditionFalse,
			LastHeartbeatTime:  metav1.Now(),
			LastTransitionTime: metav1.Now(),
			Reason:             "KubeletHasNoDiskPressure",
			Message:            "kubelet has no disk pressure",
		},
		{
			Type:               "NetworkUnavailable",
			Status:             netType,
			LastHeartbeatTime:  metav1.Now(),
			LastTransitionTime: metav1.Now(),
			Reason:             "RouteCreated",
			Message:            "RouteController created a route",
		},
	}
}

func GetResources() corev1.ResourceList {
	return corev1.ResourceList{
		"cpu":    resource.MustParse("10"),
		"memory": resource.MustParse("4096"),
	}
}

func PodPhase(p Provider, phase string) (corev1.PodStatus, error) {
	now := metav1.NewTime(time.Now())

	var podPhase corev1.PodPhase
	var initialized corev1.ConditionStatus
	var ready corev1.ConditionStatus
	var scheduled corev1.ConditionStatus

	switch phase {
	case "Ready":
		podPhase = corev1.PodSucceeded
		initialized = corev1.ConditionTrue
		ready = corev1.ConditionTrue
		scheduled = corev1.ConditionTrue
	case "Running":
		podPhase = corev1.PodRunning
		initialized = corev1.ConditionTrue
		ready = corev1.ConditionTrue
		scheduled = corev1.ConditionTrue
	case "Pending":
		podPhase = corev1.PodPending
		initialized = corev1.ConditionTrue
		ready = corev1.ConditionFalse
		scheduled = corev1.ConditionTrue
	case "Failed":
		podPhase = corev1.PodFailed
		initialized = corev1.ConditionFalse
		ready = corev1.ConditionFalse
		scheduled = corev1.ConditionFalse
	default:
		return corev1.PodStatus{}, fmt.Errorf("Invalid pod phase specified: %s", phase)
	}

	return corev1.PodStatus{
		Phase:     podPhase,
		HostIP:    p.internalIP,
		PodIP:     p.internalIP,
		StartTime: &now,
		Conditions: []corev1.PodCondition{
			{
				Type:   corev1.PodInitialized,
				Status: initialized,
			},
			{
				Type:   corev1.PodReady,
				Status: ready,
			},
			{
				Type:   corev1.PodScheduled,
				Status: scheduled,
			},
		},
	}, nil

}

func stringToReadCloser(s string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(s))
}

func (p *Provider) InitClientSet(ctx context.Context) error {
	if p.clientSet == nil {
		kubeconfig := os.Getenv("KUBECONFIG")

		config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return err
		}

		p.clientSet, err = kubernetes.NewForConfig(config)
		if err != nil {
			return err
		}
	}
	return nil
}

func (p *Provider) RetrievePodsFromCluster(ctx context.Context) error {
	namespaces, err := p.clientSet.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}

	for _, ns := range namespaces.Items {
		podsList, err := p.clientSet.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{})
		if err != nil {
			return err
		}
		for _, pod := range podsList.Items {

			if CheckAnnotatedPod(&pod, "jobId") && pod.Spec.NodeName == p.node.Name {
				fullName := pod.Namespace + "/" + pod.Spec.Containers[0].Name
				p.perianPods[fullName] = PerianPod{
					jobId: pod.Annotations["jobId"],
					pod:   &pod,
				}
				p.notifier(&pod)
			}
		}
	}
	return nil
}

func CheckAnnotatedPod(pod *corev1.Pod, key string) bool {
	_, ok := pod.Annotations[key]
	return ok
}
