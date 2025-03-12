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

// GetProvierLabels returns the Kubernetes virtual node labels.
func GetProviderNodeLabels(nodeName string) map[string]string {
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

// InitPerianJobClient initializes a Perian sky platform HTTP client for the Job service.
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

// GetNodeTaints returns the Kubernetes virtual node taints.
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

// CreateKubeNode creates a new Kubernetes virtual node.
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

// CreateNodeObjectMeta returns ObjectMeta for creating a new node.
func CreateNodeObjectMeta(nodeName string, labels map[string]string) metav1.ObjectMeta {
	objectMeta := metav1.ObjectMeta{
		Name:   nodeName,
		Labels: labels,
	}
	return objectMeta
}

// CreateNodeSpec returns NodeSpec for creating a new node.
func CreateNodeSpec(nodeName string, taints []corev1.Taint) corev1.NodeSpec {
	spec := corev1.NodeSpec{
		ProviderID: "external:///" + nodeName,
		Taints:     taints,
	}
	return spec
}

// CreateNodeStatus returns node status for a new node.
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

// InitProvider initializes a Perian provider object.
func InitProvider(
	jobClient *perian_client.JobAPIService,
	operatingSystem string,
	node corev1.Node,
	kubeletPort int32,
	internalIP string,
	kubeClient *kubernetes.Clientset,
	kubernetesSecretName string,
) Provider {
	provider := Provider{
		jobClient:            jobClient,
		operatingSystem:      operatingSystem,
		node:                 &node,
		kubeletPort:          kubeletPort,
		internalIP:           internalIP,
		clientSet:            kubeClient,
		kubernetesSecretName: kubernetesSecretName,
	}
	provider.perianPods = make(map[string]PerianPod)
	return provider
}

// CreatePerianJob creates a job on the Perian sky platform given the resource requirements, docker image, and docker
// registry credentials.
func CreatePerianJob(
	ctx context.Context,
	jobClient *perian_client.JobAPIService,
	imageName string,
	dockerSecret *DockerSecret,
	jobResources JobResources,
) (string, error) {
	createJobAPI := jobClient.CreateJob(ctx)
	createJobRequest := perian_client.NewCreateJobRequest()
	dockerRegistryCredentials := GetRequestDockerRegistryCredentials(dockerSecret)
	dockerRunParameters := GetRequestDockerRunParameters(imageName)
	instanceTypeQuery := GetRequestInstanceTypeQuery(jobResources)
	if dockerRegistryCredentials != nil {
		createJobRequest.SetDockerRegistryCredentials(*dockerRegistryCredentials)
	}
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

// GetRequestDockerRegistryCredentials returns the docker registry credentials for the create job request.
func GetRequestDockerRegistryCredentials(dockerSecret *DockerSecret) *perian_client.DockerRegistryCredentials {
	if dockerSecret != nil {
		dockerRegistryCredentials := perian_client.NewDockerRegistryCredentials()
		dockerRegistryCredentials.SetUrl(dockerSecret.registryURL)
		dockerRegistryCredentials.SetUsername(dockerSecret.username)
		dockerRegistryCredentials.SetPassword(dockerSecret.password)
		return dockerRegistryCredentials
	}
	return nil
}

// GetRequestDockerRunParameters returns the docker run parameters for the create job request.
func GetRequestDockerRunParameters(imageName string) perian_client.DockerRunParameters {
	dockerRunParameters := perian_client.NewDockerRunParameters()
	dockerRunParameters.SetImageName(imageName)
	return *dockerRunParameters
}

// GetRequestInstanceTypeQuery returns the instance type query for the create job request.
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

// GetCPUQuery return the CPU query for the instance type query.
func GetCpuQuery(cpu int32) perian_client.CpuQueryInput {
	cpuQueryInput := perian_client.NewCpuQueryInput()
	cpuQueryInput.SetCores(cpu)
	return *cpuQueryInput
}

// GetMemoryQuery returns the Memory query for the instance type query.
func GetMemoryQuery(memory string) perian_client.MemoryQueryInput {
	memoryQueryInput := perian_client.NewMemoryQueryInput()
	memorySize := perian_client.Size{
		String: &memory,
	}
	memoryQueryInput.SetSize(memorySize)
	return *memoryQueryInput
}

// GetAcceleratorQuery returns accelerator query for the instance type query.
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

// AnnotatePodWithJobID annotates a pod by the Perian job id.
func AnnotatePodWithJobID(pod *corev1.Pod, jobId string) {
	pod.Annotations["jobId"] = jobId
}

// SetPodStatusToPending sets a Kubernetes Pod status to pending.
func SetPodStatusToPending(pod *corev1.Pod, internalIP string) error {
	status := PodPhase("Pending", internalIP)
	pod.Status = status
	return nil
}

// PodPhase returns a Kubernetes Pod phase given the status of the Perian job.
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

// SetPodContainerStatusToInit sets the Kubernetes Pod status to initializing (Waiting).
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

// GetPodJobId queries a Pod's Perian job by ID.
func GetPodJobId(provider *Provider, name string) (string, error) {
	perianPod, ok := provider.perianPods[name]
	if !ok {
		return "", fmt.Errorf("pod with name %s was not found", name)
	}
	return perianPod.jobId, nil
}

// CancelPerianJobById cancels a Pod's Perian job by ID.
func CancelPerianJobById(ctx context.Context, jobClient perian_client.JobAPIService, id string) error {
	cancelJobAPI := jobClient.CancelJob(ctx, id)
	response, _, err := cancelJobAPI.Execute()
	if *response.StatusCode != 200 || err != nil {
		return fmt.Errorf("couldn't cancel job id %s", id)
	}
	return nil
}

// UpdateDeletedPodStatus updates the Kubernetes Pod status to Terminated after Perian job cancellation.
func UpdateDeletedPodStatus(pod *corev1.Pod) {
	pod.Status.ContainerStatuses[0].Ready = false
	pod.Status.ContainerStatuses[0].State = corev1.ContainerState{
		Terminated: &corev1.ContainerStateTerminated{
			FinishedAt: metav1.Now(),
			Message:    "perian job deleted",
		},
	}
}

// GetProviderPodList returns a list of all the pods running on the Perian VK.
func GetProviderPodList(provider *Provider) []*corev1.Pod {
	var pods []*corev1.Pod
	for _, perianPod := range provider.perianPods {
		pods = append(pods, perianPod.pod)
	}
	return pods
}

// GetPerianJobById fetches a job from the Perian sky platform by its ID.
func GetPerianJobById(ctx context.Context, jobClient perian_client.JobAPIService, id string) (*perian_client.JobView, error) {
	getJobRequest := jobClient.GetJobById(ctx, id)
	response, _, err := getJobRequest.Execute()
	if *response.StatusCode != 200 || err != nil {
		return nil, fmt.Errorf("couldn't get logs for job id %s", id)
	}
	return &response.Jobs[0], nil
}

// GetPerianJobLogs fetches the logs of a Perian job by its ID.
func GetPerianJobLogs(ctx context.Context, jobClient perian_client.JobAPIService, id string) (string, error) {
	perianJob, err := GetPerianJobById(ctx, jobClient, id)
	if err != nil {
		return "", err
	}
	logs := perianJob.Logs.Get()
	return *logs, nil
}

// CheckAnnotatedPod checks if a Kubernetes Pod is annotated by the Perian job ID or not.
func CheckAnnotatedPod(pod *corev1.Pod, key string) bool {
	_, ok := pod.Annotations[key]
	return ok
}

// GeneratePodKey generates a Kubernetes Pod key from the Pod namespace and name.
func GeneratePodKey(pod *corev1.Pod) string {
	return CreateKeyFromNamespaceAndName(pod.Namespace, pod.Name)
}

// CreateKeyFromNamespaceAndName creates a key given the namespace and the name.
func CreateKeyFromNamespaceAndName(namespace string, name string) string {
	return namespace + "/" + name
}

// GetPodImageName returns the Kubernetes Pod image name for the first container.
func GetPodImageName(pod *corev1.Pod) string {
	return pod.Spec.Containers[0].Image
}

// GetPodImageName returns the Kubernetes Pod container name for the first container.
func GetPodContainerName(pod *corev1.Pod) string {
	return pod.Spec.Containers[0].Name
}

// GetDockerSecrets gets docker registry credentials from the Kubernetes Secrets.
func GetDockerSecrets(ctx context.Context, clientSet *kubernetes.Clientset, kubernetesSecretName string) (*DockerSecret, error) {
	dockerSecret, err := clientSet.CoreV1().Secrets("default").Get(ctx, kubernetesSecretName, metav1.GetOptions{})
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

// GetJobResources extracts the required resources for a Pod container to run.
func GetJobResources(pod *corev1.Pod) JobResources {
	requestedResources := pod.Spec.Containers[0].Resources.Requests
	cpu := requestedResources.Cpu().Value()
	memory := requestedResources.Memory().Value()
	memoryStr := GetMemoryGb(memory)
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

// GetMemoryGb returns a ceiled integer of the required Memory in Gb formatted to string.
func GetMemoryGb(bytes int64) string {
	gb := float64(bytes) / 1073741824
	roundedGb := int(math.Ceil(gb))
	return strconv.Itoa(roundedGb)
}

// stringToReadCloser formats a given string into a ReadCloser object.
func stringToReadCloser(s string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(s))
}

// GetNodeCondition returns a list of node conditions describing whether it is ready or not.
func GetNodeCondition(ready bool) []corev1.NodeCondition {
	nodeConditionsList := []corev1.NodeCondition{
		CreateReadyNodeCondition(ready),
		CreateOutOfDiskNodecondition(),
		CreateMemoryPressureNodeCondition(),
		CreateDiskPressureNodeCondition(),
		CreateNetworkUnavailableNodeCondition(ready),
	}
	return nodeConditionsList
}

// CreateReadyNodeCondition returns a node condition describing if the node is ready or not.
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

// CreateOutOfDiskNodecondition returns a node condition for the OutOfDisk condition set to false.
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

// CreateMemoryPressureNodeCondition returns a node condition for the MemoryPressure condition set to false.
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

// CreateDiskPressureNodeCondition returns a node condition for the DiskPressure condition set to false.
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

// CreateNetworkUnavailableNodeCondition returns a node condition for the NetworkUnavailable whether the node is ready or not.
func CreateNetworkUnavailableNodeCondition(ready bool) corev1.NodeCondition {
	netType := corev1.ConditionTrue
	if ready {
		netType = corev1.ConditionFalse
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

// GetJobStatus returns the status of a Perian job.
func GetJobStatus(job perian_client.JobView) perian_client.JobStatus {
	return *job.Status
}

// IsPodSucceeded checks the phase of a Kubernetes Pod if it is succeeded or not.
func IsPodSucceeded(pod *corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodSucceeded
}

// SetPodStatus sets the Kubernetes Pod status depending on the Perian job status.
func SetPodStatus(pod *corev1.Pod, internalIP string, status perian_client.JobStatus, createdAt *time.Time, doneAt *time.Time) {
	pod.Status = PodPhase(status, internalIP)
	containerName := GetPodContainerName(pod)
	containerImage := GetPodImageName(pod)
	pod.Status.ContainerStatuses = GetContainerStatuses(containerName, containerImage, status, createdAt, doneAt)
}

// GetContainerStatuses returns the Pod Container status.
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

// GetContainerStatusState checks and returns the Pod Container status depending on the Perian job status.
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

// IsStatusDone checks if the Perian job status is done.
func IsStatusDone(status perian_client.JobStatus) bool {
	return status == perian_client.JOBSTATUS_DONE
}

// IsStatusInitializing checks if the Perian job status is either initializing or queued.
func IsStatusInitializing(status perian_client.JobStatus) bool {
	return status == perian_client.JOBSTATUS_INITIALIZING || status == perian_client.JOBSTATUS_QUEUED
}

// IsStatusRunning checks if the Perian job status is running.
func IsStatusRunning(status perian_client.JobStatus) bool {
	return status == perian_client.JOBSTATUS_RUNNING
}

// IsStatusCancelled checks if the Perian job status is cancelled.
func IsStatusCancelled(status perian_client.JobStatus) bool {
	return status == perian_client.JOBSTATUS_CANCELLED
}

// GetContainerStateReady returns a container state (Terminated) with a StartedAt and FinishedAt timestamps and exitcode 0.
func GetContainerStateReady(createdAt *time.Time, doneAt *time.Time) corev1.ContainerState {
	return corev1.ContainerState{
		Terminated: &corev1.ContainerStateTerminated{
			StartedAt:  metav1.Time{Time: *createdAt},
			FinishedAt: metav1.Time{Time: *doneAt},
			ExitCode:   0,
		},
	}
}

// GetContainerStateInitializing returns a container state (Waiting).
func GetContainerStateInitializing() corev1.ContainerState {
	return corev1.ContainerState{
		Waiting: &corev1.ContainerStateWaiting{
			Reason: "Job is initializing",
		},
	}
}

// GetContainerStateRunning returns a container state (Running) with a StartedAt timestamp.
func GetContainerStateRunning(startedAt *time.Time) corev1.ContainerState {
	return corev1.ContainerState{
		Running: &corev1.ContainerStateRunning{
			StartedAt: metav1.Time{Time: *startedAt},
		},
	}
}

// GetContainerSteteFailed returns a container state (Terminated) with reason job failed.
func GetContainerStateFailed() corev1.ContainerState {
	return corev1.ContainerState{
		Terminated: &corev1.ContainerStateTerminated{
			Reason: "Job resulted in an error",
		},
	}
}

// GetContainerStateCancelled returns a container state (Terminated) with reason job cancelled.
func GetContainerStateCancelled() corev1.ContainerState {
	return corev1.ContainerState{
		Terminated: &corev1.ContainerStateTerminated{
			Reason: "Job is cancelled",
		},
	}
}

// GetContainerReady checks if a container is ready or not.
func GetContainerReady(status perian_client.JobStatus) bool {
	return status == perian_client.JOBSTATUS_DONE
}

// GetClusterNamespaces returns all the namespaces in the Kubernetes cluster.
func GetClusterNamespaces(ctx context.Context, clientSet *kubernetes.Clientset) (corev1.NamespaceList, error) {
	namespaces, err := clientSet.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	return *namespaces, err
}

// GetNamespacePodsList returns all Kubernetes Pods in a namespace.
func GetNamespacePodsList(ctx context.Context, clientSet *kubernetes.Clientset, namespace string) (corev1.PodList, error) {
	podsList, err := clientSet.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	return *podsList, err
}

// IsPodRunningOnNode checks if the Kubernetes Pod running on a certain Node.
func IsPodRunningOnNode(pod *corev1.Pod, node *corev1.Node) bool {
	return pod.Spec.NodeName == node.Name
}
