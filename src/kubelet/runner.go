package perian

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/virtual-kubelet/virtual-kubelet/log"
	logruslogger "github.com/virtual-kubelet/virtual-kubelet/log/logrus"
	"github.com/virtual-kubelet/virtual-kubelet/node"
	"github.com/virtual-kubelet/virtual-kubelet/node/api"
	"gopkg.in/yaml.v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	lease "k8s.io/client-go/kubernetes/typed/coordination/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/record"
)

// RunPerianVirtualKubelet runs the virtual Kubelet webserver.
//
// It fetches the Perian VK config file and the Kubernetes cluster config and initializes a virtual Kubernetes node,
// runs Kubernetes PodController and Informers.
func RunPerianVirtualKubelet(ctx context.Context) {
	InitLogger()
	perianConfig := LoadConfigFile(ctx)
	mux := InitAndRunHTTPServer(ctx, perianConfig.KubeletPort)
	kubeConfig := GetKubeConfig(ctx)
	kubeClient := CreateKubeClient(kubeConfig)
	nodeProvider := InitPerianProvider(ctx, perianConfig, kubeClient)
	InitAndRunNodeController(ctx, nodeProvider, kubeClient, kubeConfig)
	EventRecorder := InitEventRecorder(perianConfig.NodeName)
	informerFactory := InitInformerFactory(kubeClient, perianConfig.NodeName)
	podControllerConfig := InitPodControllerConfig(kubeClient, nodeProvider, EventRecorder, informerFactory)
	stopper := make(chan struct{})
	defer close(stopper)
	go informerFactory.Start(stopper)
	if !cache.WaitForCacheSync(stopper, informerFactory.Core().V1().Pods().Informer().HasSynced) {
		log.G(ctx).Fatal(fmt.Errorf("timed out waiting for caches to sync"))
		return
	}
	AttachPodHandlerRoutes(nodeProvider, mux)
	RunPodController(ctx, podControllerConfig)
}

// InitLogger initializes the logger and sets logging level to debug.
func InitLogger() {
	logger := logrus.StandardLogger()
	logger.SetLevel(logrus.DebugLevel)
	log.L = logruslogger.FromLogrus(logrus.NewEntry(logger))
}

// LoadConfig loads the Perian VK configuration and checks if VK is running in a Pod then it assigns it the Pod IP.
func LoadConfig(ctx context.Context) (config Config, err error) {
	configPath := os.Getenv("CONFIG")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return config, err
	}
	config = Config{}
	err = yaml.Unmarshal(data, &config)

	if err != nil {
		return config, err
	}

	podIp := os.Getenv("POD_IP")

	if podIp != "" {
		config.InternalIP = podIp
	}

	if config.PerianServerURL == "" {
		config.PerianServerURL = "https://api.perian.cloud"
	}
	return config, nil
}

// LoadConfigFile loads the Perian VK configurations and checks if an error has occured.
func LoadConfigFile(ctx context.Context) Config {
	config, err := LoadConfig(ctx)
	if err != nil {
		log.G(ctx).Fatal("Can not load config file")
		os.Exit(1)
	}
	return config
}

// InitAndRunHTTPServer creates and runs an HTTP server to handle requests from the Kubernetes controllers.
func InitAndRunHTTPServer(ctx context.Context, kubeletPort int32) *http.ServeMux {
	mux := http.NewServeMux()
	server := &http.Server{
		Addr:              fmt.Sprintf("0.0.0.0:%d", kubeletPort),
		Handler:           mux,
		ReadTimeout:       30 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		log.G(ctx).Infof("Starting the virtual kubelet HTTP server listening on %q", server.Addr)
		err := server.ListenAndServe()
		if err != nil {
			log.G(ctx).Fatal("Error running HTTP server: ", err.Error())
			os.Exit(1)
		}
	}()
	return mux
}

// InitPerianProvider Creates a Perian provider object and assigns it a local Kubernetes client and the data fetched from
// the Perian VK configurations.
func InitPerianProvider(ctx context.Context, config Config, kubeClient *kubernetes.Clientset) *Provider {
	nodeProvider, err := NewProvider(
		config.NodeName,
		"linux",
		config.PerianServerURL,
		config.PerianOrg,
		config.PerianAuthToken,
		config.KubeletPort,
		kubeClient,
		config.InternalIP,
		config.KubernetesSecretName,
	)
	if err != nil {
		log.G(ctx).Fatal("Error creating a new provider object: ", err.Error())
		os.Exit(1)
	}
	return nodeProvider
}

// GetKubeConfig fetches the Kubernetes cluster configurations.
func GetKubeConfig(ctx context.Context) *rest.Config {
	var kubeConfig *rest.Config
	kubeConfigFile, err := os.ReadFile(os.Getenv("KUBECONFIG"))
	if err != nil {
		if os.Getenv("KUBECONFIG") != "" {
			log.G(ctx).Debug(err)
		}
		log.G(ctx).Info("Trying InCluster configuration")

		kubeConfig, err = rest.InClusterConfig()
		if err != nil {
			log.G(ctx).Fatal(err)
		}
	} else {
		log.G(ctx).Debug("Loading Kubeconfig from " + os.Getenv("KUBECONFIG"))
		clientCfg, err := clientcmd.NewClientConfigFromBytes(kubeConfigFile)
		if err != nil {
			log.G(ctx).Fatal(err)
		}
		kubeConfig, err = clientCfg.ClientConfig()
		if err != nil {
			log.G(ctx).Fatal(err)
		}
	}
	return kubeConfig
}

// CreateKubeClient creates a local Kubernetes client.
func CreateKubeClient(kubeConfig *rest.Config) *kubernetes.Clientset {
	return kubernetes.NewForConfigOrDie(kubeConfig)
}

// InitAndRunNodeController initializes and runs a Kubernetes node controller.
func InitAndRunNodeController(
	ctx context.Context,
	provider *Provider,
	kubeClient *kubernetes.Clientset,
	kubeConfig *rest.Config,
) {
	nodeController := InitNodeController(ctx, provider, kubeClient, kubeConfig)
	go func() {
		err := nodeController.Run(ctx)
		if err != nil {
			log.G(ctx).Fatal("Error running node controller: ", err.Error())
			os.Exit(1)
		}
	}()
}

// InitNodeController initializes a node controller for the VK node.
func InitNodeController(ctx context.Context, provider *Provider, kubeClient *kubernetes.Clientset, kubecfg *rest.Config) *node.NodeController {
	nodeController, err := node.NewNodeController(
		provider,
		provider.GetNode(),
		kubeClient.CoreV1().Nodes(),
		node.WithNodeEnableLeaseV1(
			lease.NewForConfigOrDie(kubecfg).Leases(corev1.NamespaceNodeLease),
			300,
		),
	)
	if err != nil {
		log.G(ctx).Fatal("Error initializing a new node controller: ", err.Error())
		os.Exit(1)
	}
	return nodeController
}

// InitEventRecorder initializes and returns a Kubernetes event recorder.
func InitEventRecorder(nodeName string) record.EventRecorderLogger {
	eventBroadcaster := record.NewBroadcaster()
	eventRecorder := eventBroadcaster.NewRecorder(
		scheme.Scheme,
		corev1.EventSource{
			Component: path.Join(nodeName, "pod-controller"),
		},
	)
	return eventRecorder
}

// InitInformerFactory initializes and returns a Kubernetes informer factory.
func InitInformerFactory(
	kubeClient *kubernetes.Clientset,
	nodeName string,
) informers.SharedInformerFactory {
	resyncDuration, _ := time.ParseDuration("30s")

	informerFactory := informers.NewSharedInformerFactoryWithOptions(
		kubeClient,
		resyncDuration,
		PodInformerFilter(nodeName),
	)

	return informerFactory
}

// InitResyncDuration initializes and returns the resync duration for the informer facotry.
func InitResyncDuration(ctx context.Context) time.Duration {
	resyncDuration, err := time.ParseDuration("30s")
	if err != nil {
		log.G(ctx).Fatal("Error parsing duration string: ", err.Error())
		os.Exit(1)
	}
	return resyncDuration
}

// PodInformerFilter creates a filter to select a node by name for the informer factory.
func PodInformerFilter(node string) informers.SharedInformerOption {
	return informers.WithTweakListOptions(func(options *metav1.ListOptions) {
		options.FieldSelector = fields.OneTermEqualSelector("spec.nodeName", node).String()
	})
}

// RunPodController creates and runs a new PodController.
func RunPodController(ctx context.Context, config node.PodControllerConfig) {
	podController, err := node.NewPodController(config)
	if err != nil {
		log.G(ctx).Fatal(err)
	}
	err = podController.Run(ctx, 1) // <-- starts watching for pods to be scheduled on the node
	if err != nil {
		log.G(ctx).Fatal(err)
	}
}

// InitPodControllerConfig initializes the configurations for a PodController.
func InitPodControllerConfig(
	kubeClient *kubernetes.Clientset,
	provider *Provider,
	eventRecorder record.EventRecorder,
	informerFactory informers.SharedInformerFactory,
) node.PodControllerConfig {
	config := node.PodControllerConfig{
		PodClient:         kubeClient.CoreV1(),
		Provider:          provider,
		EventRecorder:     eventRecorder,
		PodInformer:       informerFactory.Core().V1().Pods(),
		SecretInformer:    informerFactory.Core().V1().Secrets(),
		ConfigMapInformer: informerFactory.Core().V1().ConfigMaps(),
		ServiceInformer:   informerFactory.Core().V1().Services(),
	}
	return config
}

// AttachPodHandlerRoutes attaches the PodHandler routes to the running HTTP server.
func AttachPodHandlerRoutes(provider *Provider, mux *http.ServeMux) {
	config := InitPodHandlerConfig(provider)
	api.AttachPodRoutes(config, mux, true)
}

// InitPodHandlerConfig initializes a PodHandler configurations.
func InitPodHandlerConfig(provider *Provider) api.PodHandlerConfig {
	config := api.PodHandlerConfig{
		GetContainerLogs: provider.GetLogs,
		GetPods:          provider.GetPods,
	}
	return config
}
