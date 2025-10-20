package restorer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

func RestoreFromConfigMaps(clientset *kubernetes.Clientset, name, outputDir, namespace string) error {
	listOptions := metav1.ListOptions{}
	configMaps, err := clientset.CoreV1().ConfigMaps(namespace).List(context.TODO(), listOptions)
	if err != nil {
		return fmt.Errorf("failed to list ConfigMaps: %w", err)
	}

	for _, cm := range configMaps.Items {
		if !strings.HasPrefix(cm.Name, name) {
			continue
		}

		for path, content := range cm.BinaryData {
			fullPath := filepath.Join(outputDir, path)
			if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", filepath.Dir(fullPath), err)
			}
			if err := os.WriteFile(fullPath, content, 0644); err != nil {
				return fmt.Errorf("failed to write file %s: %w", fullPath, err)
			}
			fmt.Printf("Restored %s\n", fullPath)
		}
	}

	return nil
}
