package kube

import (
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func GetClientset() (*kubernetes.Clientset, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

	config, err := kubeConfig.ClientConfig()
	if err != nil {
		return nil, err
	}

	return kubernetes.NewForConfig(config)
}

func ApplyConfigMap(clientset *kubernetes.Clientset, cm v1.ConfigMap, namespace string) error {
	_, err := clientset.CoreV1().ConfigMaps(namespace).Get(context.TODO(), cm.Name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			_, err = clientset.CoreV1().ConfigMaps(namespace).Create(context.TODO(), &cm, metav1.CreateOptions{}) 
			if err != nil {
				return fmt.Errorf("failed to create ConfigMap: %w", err)
			}
			fmt.Printf("ConfigMap %s created\n", cm.Name)
		} else {
			return fmt.Errorf("failed to get ConfigMap: %w", err)
		}
	} else {
		_, err = clientset.CoreV1().ConfigMaps(namespace).Update(context.TODO(), &cm, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to update ConfigMap: %w", err)
		}
		fmt.Printf("ConfigMap %s updated\n", cm.Name)
	}
	return nil
}
