package perian

import (
	"context"
	"flag"
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
	cache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/record"
)

func RunPerianVirtualKubelet(ctx context.Context) {
	perianConfig := LoadConfigFile(ctx)
	InitLogger()
	InitAndRunHTTPServer(perianConfig.KubeletPort)
	kubeConfig := GetKubeConfig(ctx)
	kubeClient := CreateKubeClient(kubeConfig)
	provider := InitPerianProvider(perianConfig)
	InitAndRunNodeController(ctx, provider, kubeClient, kubeConfig)
	eventRecorder := InitEventRecorder(perianConfig.NodeName)
	resyncDuration := InitResyncDuration()
	podInformerFactory, scmInformerFactory := InitInformerFactories(
		kubeClient,
		resyncDuration,
		perianConfig.NodeName,
	)
	podInformer, scmInformer := InitInformers(podInformerFactory, scmInformerFactory)
	StartInformers(ctx, podInformerFactory, scmInformerFactory, podInformer, scmInformer)
	InitAndRunPodController(ctx, kubeClient, provider, eventRecorder, podInformerFactory, scmInformerFactory)
}

func LoadConfig(ctx context.Context, configFile string) (config Config, err error) {
	data, err := os.ReadFile(configFile)

	if err != nil {
		return config, err
	}

	config = Config{}
	err = yaml.Unmarshal(data, &config)

	if err != nil {
		return config, err
	}

	return config, nil
}

func ReadConfigPath() string {
	flagPath := flag.String("configpath", "", "Path to the virtual kubelet config file.")
	flag.Parse()
	var configPath string
	if *flagPath != "" {
		configPath = *flagPath
	} else {
		panic(fmt.Errorf("You must specify a config file path."))
	}
	return configPath
}

func LoadConfigFile(ctx context.Context) Config {
	configPath := ReadConfigPath()
	config, err := LoadConfig(ctx, configPath)
	if err != nil {
		log.G(ctx).Error("cannot load config file at", configPath)
		panic(fmt.Errorf(err.Error()))
	}
	return config
}

func InitLogger() {
	logger := logrus.StandardLogger()
	logger.SetLevel(logrus.DebugLevel)
	log.L = logruslogger.FromLogrus(logrus.NewEntry(logger))
}

func InitAndRunHTTPServer(kubeletPort int32) *http.ServeMux {
	mux := http.NewServeMux()
	server := &http.Server{
		Addr:              fmt.Sprintf("0.0.0.0:%d", kubeletPort),
		Handler:           mux,
		ReadTimeout:       30 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := server.ListenAndServe(); err != nil {
			os.Exit(1)
		}
	}()
	return mux
}

func InitPerianProvider(config Config) *Provider {
	nodeProvider, err := NewProvider(
		config.NodeName,
		"linux",
		config.PerianServerURL,
		config.PerianOrg,
		config.PerianAuthToken,
		config.KubeletPort,
	)
	if err != nil {
		panic(err.Error())
	}
	return nodeProvider
}

func GetKubeConfig(ctx context.Context) *rest.Config {
	var kubecfg *rest.Config
	kubecfgFile, err := os.ReadFile(os.Getenv("KUBECONFIG"))
	if err != nil {
		if os.Getenv("KUBECONFIG") != "" {
			log.G(ctx).Debug(err)
		}
		log.G(ctx).Info("Trying InCluster configuration")

		kubecfg, err = rest.InClusterConfig()
		if err != nil {
			log.G(ctx).Fatal(err)
		}
	} else {
		log.G(ctx).Debug("Loading Kubeconfig from " + os.Getenv("KUBECONFIG"))
		clientCfg, err := clientcmd.NewClientConfigFromBytes(kubecfgFile)
		if err != nil {
			log.G(ctx).Fatal(err)
		}
		kubecfg, err = clientCfg.ClientConfig()
		if err != nil {
			log.G(ctx).Fatal(err)
		}
	}
	return kubecfg
}

func CreateKubeClient(kubeConfig *rest.Config) *kubernetes.Clientset {
	return kubernetes.NewForConfigOrDie(kubeConfig)
}

func InitAndRunNodeController(
	ctx context.Context,
	provider *Provider,
	kubeClient *kubernetes.Clientset,
	kubeConfig *rest.Config,
) {
	nodeController := InitNodeController(provider, kubeClient, kubeConfig)
	go func() {
		err := nodeController.Run(ctx)
		if err != nil {
			panic(fmt.Errorf(err.Error()))
		}
	}()
}

func InitNodeController(provider *Provider, kubeClient *kubernetes.Clientset, kubecfg *rest.Config) *node.NodeController {
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
		panic(fmt.Errorf(err.Error()))
	}
	return nodeController
}

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

func InitInformerFactories(kubeClient *kubernetes.Clientset, resyncDuration time.Duration, nodeName string) (informers.SharedInformerFactory, informers.SharedInformerFactory) {
	podInformerFactory := informers.NewSharedInformerFactoryWithOptions(
		kubeClient,
		resyncDuration,
		PodInformerFilter(nodeName),
	)
	scmInformerFactory := informers.NewSharedInformerFactoryWithOptions(
		kubeClient,
		resyncDuration,
	)
	return podInformerFactory, scmInformerFactory
}

func InitResyncDuration() time.Duration {
	resyncDuration, err := time.ParseDuration("30s")
	if err != nil {
		panic(fmt.Errorf("couldn't parse duration"))
	}
	return resyncDuration
}

func PodInformerFilter(node string) informers.SharedInformerOption {
	return informers.WithTweakListOptions(func(options *metav1.ListOptions) {
		options.FieldSelector = fields.OneTermEqualSelector("spec.nodeName", node).String()
	})
}

func InitInformers(podInformerFactory informers.SharedInformerFactory, scmInformerFactory informers.SharedInformerFactory) (cache.SharedIndexInformer, cache.SharedIndexInformer) {
	scmInformer := scmInformerFactory.Core().V1().Secrets().Informer()
	podInformer := podInformerFactory.Core().V1().Pods().Informer()
	return scmInformer, podInformer
}

func StartInformers(
	ctx context.Context,
	podInformerFactory informers.SharedInformerFactory,
	scmInformerFactory informers.SharedInformerFactory,
	podInformer cache.SharedIndexInformer,
	scmInformer cache.SharedIndexInformer,
) {
	stopper := make(chan struct{})
	defer close(stopper)
	go podInformerFactory.Start(stopper)
	go scmInformerFactory.Start(stopper)
	go scmInformer.Run(stopper)
	go podInformer.Run(stopper)
	if !cache.WaitForCacheSync(stopper, podInformerFactory.Core().V1().Pods().Informer().HasSynced) {
		log.G(ctx).Fatal(fmt.Errorf("timed out waiting for caches to sync"))
		return
	}
}

func InitAndRunPodController(
	ctx context.Context,
	kubeClient *kubernetes.Clientset,
	provider *Provider,
	eventRecorder record.EventRecorder,
	podInformerFactory informers.SharedInformerFactory,
	scmInformerFactory informers.SharedInformerFactory,
) {
	config := InitPodControllerConfig(kubeClient, provider, eventRecorder, podInformerFactory, scmInformerFactory)
	podController, err := node.NewPodController(config)
	if err != nil {
		log.G(ctx).Fatal(err)
	}
	err = podController.Run(ctx, 1) // <-- starts watching for pods to be scheduled on the node
	if err != nil {
		log.G(ctx).Fatal(err)
	}
}

func InitPodControllerConfig(
	kubeClient *kubernetes.Clientset,
	provider *Provider,
	eventRecorder record.EventRecorder,
	podInformerFactory informers.SharedInformerFactory,
	scmInformerFactory informers.SharedInformerFactory,
) node.PodControllerConfig {
	config := node.PodControllerConfig{
		PodClient:         kubeClient.CoreV1(),
		Provider:          provider,
		EventRecorder:     eventRecorder,
		PodInformer:       podInformerFactory.Core().V1().Pods(),
		SecretInformer:    scmInformerFactory.Core().V1().Secrets(),
		ConfigMapInformer: scmInformerFactory.Core().V1().ConfigMaps(),
		ServiceInformer:   scmInformerFactory.Core().V1().Services(),
	}
	return config
}

func AttachPodHandlerRoutes(provider *Provider, mux *http.ServeMux) {
	config := InitPodHandlerConfig(provider)
	api.AttachPodRoutes(config, mux, true)
}

func InitPodHandlerConfig(provider *Provider) api.PodHandlerConfig {
	config := api.PodHandlerConfig{
		GetContainerLogs: provider.GetLogs,
		GetStatsSummary:  provider.GetStatsSummary,
		GetPods:          provider.GetPods,
	}
	return config
}
