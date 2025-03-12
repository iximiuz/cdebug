package kubernetes

import (
	"fmt"
	"os"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
		_ "k8s.io/client-go/plugin/pkg/client/auth/oidc"
)

func GetRESTConfig(
	apiServer string,
	kubeconfig string,
	kubeconfigContext string,
) (*rest.Config, string, error) {
	if apiServer != "" {
		return &rest.Config{
			Host: apiServer,
		}, "", nil
	}

	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		config, err := rest.InClusterConfig()
		if err != nil {
			return nil, "", fmt.Errorf("error loading in-cluster kubeconfig: %v", err)
		}
		return config, "", nil
	}

	if kubeconfig == "" {
		kubeconfig = clientcmd.NewDefaultClientConfigLoadingRules().GetDefaultFilename()
	}

	configLoader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{
			ExplicitPath: kubeconfig,
		},
		&clientcmd.ConfigOverrides{
			CurrentContext: kubeconfigContext,
		},
	)

	config, err := configLoader.ClientConfig()
	if err != nil {
		return nil, "", fmt.Errorf("error loading kubeconfig: %v", err)
	}

	namespace, _, err := configLoader.Namespace()
	if err != nil {
		return nil, "", fmt.Errorf("error getting namespace from kubeconfig: %v", err)
	}

	return config, namespace, nil
}
