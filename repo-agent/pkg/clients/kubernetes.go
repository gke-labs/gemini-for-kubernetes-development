package clients

import (
	"fmt"
	"os"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// KubernetesClient holds Kubernetes clients and configuration.
type KubernetesClient struct {
	RestConfig       *rest.Config
	Clientset        *kubernetes.Clientset
	DynamicClient    dynamic.Interface
	CurrentNamespace string
}

// NewKubernetesClient creates a new KubernetesClient.
// It attempts to load configuration in the following order:
// 1. In-cluster configuration (if running inside a Pod)
// 2. KUBECONFIG environment variable
// 3. Default local kubeconfig (~/.kube/config)
func NewKubernetesClient() (*KubernetesClient, error) {
	var config *rest.Config
	var err error
	var namespace string

	// 1. Try In-Cluster Config
	config, err = rest.InClusterConfig()
	if err == nil {
		// In-cluster: Try to read namespace from service account secret
		nsBytes, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
		if err == nil {
			namespace = string(nsBytes)
		} else {
			namespace = "default"
		}
	} else {
		// 2. Fallback to Local Config (KUBECONFIG or default)
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		configOverrides := &clientcmd.ConfigOverrides{}
		kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

		config, err = kubeConfig.ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
		}

		namespace, _, err = kubeConfig.Namespace()
		if err != nil {
			namespace = "default"
		}
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes clientset: %w", err)
	}

	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	return &KubernetesClient{
		RestConfig:       config,
		Clientset:        clientset,
		DynamicClient:    dynamicClient,
		CurrentNamespace: namespace,
	}, nil
}
