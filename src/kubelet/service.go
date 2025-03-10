package perian

import (
	"context"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	perian_client "github.com/Perian-io/perian-go-client"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

func GetProviderLabels(nodeName string) map[string]string {
	labels := map[string]string{
		"alpha.service-controller.kubernetes.io/exclude-balancer": "true",
		"beta.kubernetes.io/os":                                   "linux",
		"kubernetes.io/os":                                        "linux",
		"kubernetes.io/hostname":                                  nodeName,
		"kubernetes.io/role":                                      "agent",
		"node.kubernetes.io/exclude-from-external-load-balancers": "true",
		"virtual-node.perian/type":                                "virtual-kubelet",
	}
	return labels
}

func InitPerianJobClient(url string, token string, organization string) *perian_client.JobAPIService {
	config := perian_client.NewConfiguration()
	config.Servers = perian_client.ServerConfigurations{
		{
			URL: url,
		},
	}
	config.AddDefaultHeader("Authorization", token)
	config.AddDefaultHeader("X-PERIAN-AUTH-ORG", organization)
	apiClient := perian_client.NewAPIClient(config)

	jobClient := apiClient.JobAPI
	return jobClient
}

func GetNodeTaints() []corev1.Taint {
	taints := []corev1.Taint{
		{
			Key:    "virtual-node.perian/no-schedule",
			Value:  strconv.FormatBool(false),
			Effect: corev1.TaintEffectNoSchedule,
		},
	}
	return taints
}

func CreateKubeNode(
	nodeName string,
	labels map[string]string,
	taints []corev1.Taint,
	internalIP string,
	kubeletPort int32,
) corev1.Node {
	node := corev1.Node{
		ObjectMeta: CreateNodeObjectMeta(nodeName, labels),
		Spec:       CreateNodeSpec(nodeName, taints),
		Status:     CreateNodeStatus(internalIP, kubeletPort),
	}
	return node
}

func CreateNodeObjectMeta(nodeName string, labels map[string]string) metav1.ObjectMeta {
	objectMeta := metav1.ObjectMeta{
		Name:   nodeName,
		Labels: labels,
	}
	return objectMeta
}

func CreateNodeSpec(nodeName string, taints []corev1.Taint) corev1.NodeSpec {
	spec := corev1.NodeSpec{
		ProviderID: "external:///" + nodeName,
		Taints:     taints,
	}
	return spec
}

func CreateNodeStatus(internalIP string, kubeletPort int32) corev1.NodeStatus {
	status := corev1.NodeStatus{
		NodeInfo: corev1.NodeSystemInfo{
			KubeletVersion:  "v1.32.0",
			Architecture:    "virtual-kubelet",
			OperatingSystem: "linux",
		},
		Addresses:       []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: internalIP}},
		DaemonEndpoints: corev1.NodeDaemonEndpoints{KubeletEndpoint: corev1.DaemonEndpoint{Port: kubeletPort}},
		Conditions:      GetNodeCondition(false),
	}
	return status
}

func InitProvider(
	jobClient *perian_client.JobAPIService,
	operatingSystem string,
	node corev1.Node,
	kubeletPort int32,
	internalIP string,
	kubeClient *kubernetes.Clientset,

) Provider {
	provider := Provider{
		jobClient:       jobClient,
		operatingSystem: operatingSystem,
		node:            &node,
		kubeletPort:     kubeletPort,
		internalIP:      internalIP,
		clientSet:       kubeClient,
	}
	provider.perianPods = make(map[string]PerianPod)
	return provider
}

func CreatePerianJob(
	ctx context.Context,
	jobClient *perian_client.JobAPIService,
	imageName string,
	dockerSecret DockerSecret,
	jobResources JobResources,
) (string, error) {
	createJobAPI := jobClient.CreateJob(ctx)
	createJobRequest := perian_client.NewCreateJobRequest()
	dockerRegistryCredentials := GetRequestDockerRegistryCredentials(dockerSecret)
	dockerRunParameters := GetRequestDockerRunParameters(imageName)
	instanceTypeQuery := GetRequestInstanceTypeQuery(jobResources)
	createJobRequest.SetDockerRegistryCredentials(dockerRegistryCredentials)
	createJobRequest.SetDockerRunParameters(dockerRunParameters)
	createJobRequest.SetRequirements(instanceTypeQuery)
	createJobAPI = createJobAPI.CreateJobRequest(*createJobRequest)
	createJobSuccess, _, err := createJobAPI.Execute()
	if err != nil {
		return "", err
	}
	jobId := createJobSuccess.GetId()
	return jobId, nil
}

func GetRequestDockerRegistryCredentials(dockerSecret DockerSecret) perian_client.DockerRegistryCredentials {
	dockerRegistryCredentials := perian_client.NewDockerRegistryCredentials()
	dockerRegistryCredentials.SetUrl(dockerSecret.registryURL)
	dockerRegistryCredentials.SetUsername(dockerSecret.username)
	dockerRegistryCredentials.SetPassword(dockerSecret.password)
	return *dockerRegistryCredentials
}

func GetRequestDockerRunParameters(imageName string) perian_client.DockerRunParameters {
	dockerRunParameters := perian_client.NewDockerRunParameters()
	dockerRunParameters.SetImageName(imageName)
	return *dockerRunParameters
}

func GetRequestInstanceTypeQuery(jobResources JobResources) perian_client.InstanceTypeQueryInput {
	instanceTypeQuery := perian_client.NewInstanceTypeQueryInput()
	cpuQueryInput := GetCpuQuery(jobResources.cpu)
	instanceTypeQuery.SetCpu(cpuQueryInput)
	memoryQueryInput := GetMemoryQuery(jobResources.memory)
	instanceTypeQuery.SetRam(memoryQueryInput)
	acceleratorQueryInput := GetAcceleratorQuery(jobResources.accelerators, jobResources.acceleratorType)
	if acceleratorQueryInput != nil {
		instanceTypeQuery.SetAccelerator(*acceleratorQueryInput)
	}
	return *instanceTypeQuery
}

func GetCpuQuery(cpu int32) perian_client.CpuQueryInput {
	cpuQueryInput := perian_client.NewCpuQueryInput()
	cpuQueryInput.SetCores(cpu)
	return *cpuQueryInput
}

func GetMemoryQuery(memory string) perian_client.MemoryQueryInput {
	memoryQueryInput := perian_client.NewMemoryQueryInput()
	memorySize := perian_client.Size{
		String: &memory,
	}
	memoryQueryInput.SetSize(memorySize)
	return *memoryQueryInput
}

func GetAcceleratorQuery(accelerators int32, acceleratorType string) *perian_client.AcceleratorQueryInput {
	if acceleratorType != "" {
		acceleratorQuery := perian_client.NewAcceleratorQueryInput()
		acceleratorQuery.SetNo(accelerators)
		acceleratorName := perian_client.Name{
			String: &acceleratorType,
		}
		acceleratorQuery.SetName(acceleratorName)
		return acceleratorQuery
	}
	return nil
}

func AnnotatePodWithJobID(pod *corev1.Pod, jobId string) {
	pod.Annotations["jobId"] = jobId
}

func SetPodStatusToPending(pod *corev1.Pod, internalIP string) error {
	status := PodPhase("Pending", internalIP)
	pod.Status = status
	return nil
}

func PodPhase(status perian_client.JobStatus, internalIP string) corev1.PodStatus {
	now := metav1.NewTime(time.Now())

	var podPhase corev1.PodPhase
	var initialized corev1.ConditionStatus
	var ready corev1.ConditionStatus
	var scheduled corev1.ConditionStatus

	switch status {
	case perian_client.JOBSTATUS_DONE:
		podPhase = corev1.PodSucceeded
		initialized = corev1.ConditionTrue
		ready = corev1.ConditionTrue
		scheduled = corev1.ConditionTrue
	case perian_client.JOBSTATUS_RUNNING:
		podPhase = corev1.PodRunning
		initialized = corev1.ConditionTrue
		ready = corev1.ConditionTrue
		scheduled = corev1.ConditionTrue
	case perian_client.JOBSTATUS_QUEUED:
		podPhase = corev1.PodPending
		initialized = corev1.ConditionTrue
		ready = corev1.ConditionFalse
		scheduled = corev1.ConditionTrue
	case perian_client.JOBSTATUS_INITIALIZING:
		podPhase = corev1.PodPending
		initialized = corev1.ConditionTrue
		ready = corev1.ConditionFalse
		scheduled = corev1.ConditionTrue
	default:
		podPhase = corev1.PodFailed
		initialized = corev1.ConditionFalse
		ready = corev1.ConditionFalse
		scheduled = corev1.ConditionFalse
	}

	return corev1.PodStatus{
		Phase:     podPhase,
		HostIP:    internalIP,
		PodIP:     internalIP,
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
	}
}

func SetPodContainerStatusToInit(pod *corev1.Pod) {
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{
		{
			Name:         GetPodContainerName(pod),
			Image:        GetPodImageName(pod),
			Ready:        false,
			RestartCount: 0,
			State: corev1.ContainerState{
				Waiting: &corev1.ContainerStateWaiting{
					Reason: "Waiting for InitContainers",
				},
			},
		},
	}
}

func GetPodJobId(provider *Provider, name string) (string, error) {
	perianPod, ok := provider.perianPods[name]
	if !ok {
		return "", fmt.Errorf("pod with name %s was not found", name)
	}
	return perianPod.jobId, nil
}

func CancelPerianJobById(ctx context.Context, jobClient perian_client.JobAPIService, id string) error {
	cancelJobAPI := jobClient.CancelJob(ctx, id)
	response, _, err := cancelJobAPI.Execute()
	if *response.StatusCode != 200 || err != nil {
		return fmt.Errorf("couldn't cancel job id %s", id)
	}
	return nil
}

func UpdateDeletedPodStatus(pod *corev1.Pod) {
	pod.Status.ContainerStatuses[0].Ready = false
	pod.Status.ContainerStatuses[0].State = corev1.ContainerState{
		Terminated: &corev1.ContainerStateTerminated{
			FinishedAt: metav1.Now(),
			Message:    "perian job deleted",
		},
	}
}

func GetProviderPodList(provider *Provider) []*corev1.Pod {
	var pods []*corev1.Pod
	for _, perianPod := range provider.perianPods {
		pods = append(pods, perianPod.pod)
	}
	return pods
}

func GetPerianJobById(ctx context.Context, jobClient perian_client.JobAPIService, id string) (*perian_client.JobView, error) {
	getJobRequest := jobClient.GetJobById(ctx, id)
	response, _, err := getJobRequest.Execute()
	if *response.StatusCode != 200 || err != nil {
		return nil, fmt.Errorf("couldn't get logs for job id %s", id)
	}
	return &response.Jobs[0], nil
}

func GetPerianJobLogs(ctx context.Context, jobClient perian_client.JobAPIService, id string) (string, error) {
	perianJob, err := GetPerianJobById(ctx, jobClient, id)
	if err != nil {
		return "", err
	}
	logs := perianJob.Logs.Get()
	return *logs, nil
}

func CheckAnnotatedPod(pod *corev1.Pod, key string) bool {
	_, ok := pod.Annotations[key]
	return ok
}

func GeneratePodKey(pod *corev1.Pod) string {
	return CreateKeyFromNamespaceAndName(pod.Namespace, pod.Name)
}

func CreateKeyFromNamespaceAndName(namespace string, name string) string {
	return namespace + "/" + name
}

func GetPodImageName(pod *corev1.Pod) string {
	return pod.Spec.Containers[0].Image
}

func GetPodContainerName(pod *corev1.Pod) string {
	return pod.Spec.Containers[0].Name
}

func GetDockerSecrets(ctx context.Context, clientSet *kubernetes.Clientset) (*DockerSecret, error) {
	dockerSecret, err := clientSet.CoreV1().Secrets("default").Get(ctx, "docker-secret", metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	dockerRegistryURL := string(dockerSecret.Data["registry"])
	dockerUsername := string(dockerSecret.Data["username"])
	dockerPassword := string(dockerSecret.Data["password"])
	secret := DockerSecret{
		registryURL: dockerRegistryURL,
		username:    dockerUsername,
		password:    dockerPassword,
	}
	return &secret, nil
}

func GetJobResources(pod *corev1.Pod) JobResources {
	requestedResources := pod.Spec.Containers[0].Resources.Requests
	cpu := requestedResources.Cpu().Value()
	memory := requestedResources.Memory().Value()
	memoryStr := GetMemoryValueString(memory)
	accelerators, err := strconv.ParseInt(pod.Annotations["accelerators"], 10, 64)
	if err != nil {
	}
	acceleratorType := pod.Annotations["acceleratorType"]
	jobResources := JobResources{
		cpu:             int32(cpu),
		memory:          memoryStr,
		accelerators:    int32(accelerators),
		acceleratorType: acceleratorType,
	}
	return jobResources
}

func GetMemoryValueString(bytes int64) string {
	gb := float64(bytes) / 1073741824
	roundedGb := int(math.Ceil(gb))
	return strconv.Itoa(roundedGb)
}

func stringToReadCloser(s string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(s))
}

func GetNodeCondition(ready bool) []corev1.NodeCondition {
	return CreateNodeConditionList(ready)
}

func CreateNodeConditionList(ready bool) []corev1.NodeCondition {
	nodeConditionsList := []corev1.NodeCondition{
		CreateReadyNodeCondition(ready),
		CreateOutOfDiskNodecondition(),
		CreateMemoryPressureNodeCondition(),
		CreateDiskPressureNodeCondition(),
		CreateNetworkUnavailableNodeCondition(ready),
	}
	return nodeConditionsList
}

func CreateReadyNodeCondition(ready bool) corev1.NodeCondition {
	readyType := corev1.ConditionFalse
	if ready {
		readyType = corev1.ConditionTrue
	}
	nodeCondition := corev1.NodeCondition{
		Type:               "Ready",
		Status:             readyType,
		LastHeartbeatTime:  metav1.Now(),
		LastTransitionTime: metav1.Now(),
		Reason:             "KubeletPending",
		Message:            "kubelet is pending.",
	}
	return nodeCondition
}

func CreateOutOfDiskNodecondition() corev1.NodeCondition {
	return corev1.NodeCondition{
		Type:               "OutOfDisk",
		Status:             corev1.ConditionFalse,
		LastHeartbeatTime:  metav1.Now(),
		LastTransitionTime: metav1.Now(),
		Reason:             "KubeletHasSufficientDisk",
		Message:            "kubelet has sufficient disk space available",
	}
}

func CreateMemoryPressureNodeCondition() corev1.NodeCondition {
	return corev1.NodeCondition{
		Type:               "MemoryPressure",
		Status:             corev1.ConditionFalse,
		LastHeartbeatTime:  metav1.Now(),
		LastTransitionTime: metav1.Now(),
		Reason:             "KubeletHasSufficientMemory",
		Message:            "kubelet has sufficient memory available",
	}
}

func CreateDiskPressureNodeCondition() corev1.NodeCondition {
	return corev1.NodeCondition{
		Type:               "DiskPressure",
		Status:             corev1.ConditionFalse,
		LastHeartbeatTime:  metav1.Now(),
		LastTransitionTime: metav1.Now(),
		Reason:             "KubeletHasNoDiskPressure",
		Message:            "kubelet has no disk pressure",
	}
}

func CreateNetworkUnavailableNodeCondition(ready bool) corev1.NodeCondition {
	netType := corev1.ConditionFalse
	if ready {
		netType = corev1.ConditionTrue
	}
	return corev1.NodeCondition{
		Type:               "NetworkUnavailable",
		Status:             netType,
		LastHeartbeatTime:  metav1.Now(),
		LastTransitionTime: metav1.Now(),
		Reason:             "RouteCreated",
		Message:            "RouteController created a route",
	}
}

func GetJobStatus(job perian_client.JobView) perian_client.JobStatus {
	return *job.Status
}

func IsPodSucceeded(pod *corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodSucceeded
}

func SetPodStatus(pod *corev1.Pod, internalIP string, status perian_client.JobStatus, createdAt *time.Time, doneAt *time.Time) {
	pod.Status = PodPhase(status, internalIP)
	containerName := GetPodContainerName(pod)
	containerImage := GetPodImageName(pod)
	pod.Status.ContainerStatuses = GetContainerStatuses(containerName, containerImage, status, createdAt, doneAt)
}

func GetContainerStatuses(containerName string, imageName string, status perian_client.JobStatus, createdAt *time.Time, doneAt *time.Time) []corev1.ContainerStatus {
	return []corev1.ContainerStatus{
		{
			Name:         containerName,
			Image:        imageName,
			Ready:        GetContainerReady(status),
			RestartCount: 0,
			State:        GetContainerStatusState(status, createdAt, doneAt),
		},
	}
}

func GetContainerStatusState(status perian_client.JobStatus, createdAt *time.Time, doneAt *time.Time) corev1.ContainerState {
	if IsStatusDone(status) {
		return GetContainerStateReady(createdAt, doneAt)
	} else if IsStatusInitializing(status) {
		return GetContainerStateInitializing()
	} else if IsStatusRunning(status) {
		return GetContainerStateRunning(createdAt)
	} else if IsStatusCancelled(status) {
		return GetContainerStateCancelled()
	} else {
		return GetContainerStateFailed()
	}
}

func IsStatusDone(status perian_client.JobStatus) bool {
	return status == perian_client.JOBSTATUS_DONE
}

func IsStatusInitializing(status perian_client.JobStatus) bool {
	return status == perian_client.JOBSTATUS_INITIALIZING || status == perian_client.JOBSTATUS_QUEUED
}

func IsStatusRunning(status perian_client.JobStatus) bool {
	return status == perian_client.JOBSTATUS_RUNNING
}

func IsStatusCancelled(status perian_client.JobStatus) bool {
	return status == perian_client.JOBSTATUS_CANCELLED
}

func GetContainerStateReady(createdAt *time.Time, doneAt *time.Time) corev1.ContainerState {
	return corev1.ContainerState{
		Terminated: &corev1.ContainerStateTerminated{
			StartedAt:  metav1.Time{Time: *createdAt},
			FinishedAt: metav1.Time{Time: *doneAt},
			ExitCode:   0,
		},
	}
}

func GetContainerStateInitializing() corev1.ContainerState {
	return corev1.ContainerState{
		Waiting: &corev1.ContainerStateWaiting{
			Reason: "Job is initializing",
		},
	}
}

func GetContainerStateRunning(startedAt *time.Time) corev1.ContainerState {
	return corev1.ContainerState{
		Running: &corev1.ContainerStateRunning{
			StartedAt: metav1.Time{Time: *startedAt},
		},
	}
}

func GetContainerStateFailed() corev1.ContainerState {
	return corev1.ContainerState{
		Terminated: &corev1.ContainerStateTerminated{
			Reason: "Job resulted in an error",
		},
	}
}

func GetContainerStateCancelled() corev1.ContainerState {
	return corev1.ContainerState{
		Terminated: &corev1.ContainerStateTerminated{
			Reason: "Job is cancelled",
		},
	}
}

func GetContainerReady(status perian_client.JobStatus) bool {
	return status == perian_client.JOBSTATUS_DONE
}

func GetClusterNamespaces(ctx context.Context, clientSet *kubernetes.Clientset) (corev1.NamespaceList, error) {
	namespaces, err := clientSet.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	return *namespaces, err
}

func GetPodsList(ctx context.Context, clientSet *kubernetes.Clientset, namespace string) (corev1.PodList, error) {
	podsList, err := clientSet.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	return *podsList, err
}

func IsPodRunningOnNode(pod *corev1.Pod, node *corev1.Node) bool {
	return pod.Spec.NodeName == node.Name
}
