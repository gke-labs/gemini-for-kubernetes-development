package loader

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func CreateConfigMaps(directory string, maxSize int) ([]v1.ConfigMap, error) {
	var configMaps []v1.ConfigMap
	baseName := filepath.Base(directory)
	currentConfigMap := createNewConfigMap(baseName, len(configMaps), directory)
	currentSize := 0

	err := filepath.Walk(directory, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		relativePath, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}

		content, err := ioutil.ReadFile(path)
		if err != nil {
			return err
		}

		if currentSize+len(content) > maxSize && currentSize > 0 {
			configMaps = append(configMaps, *currentConfigMap)
			currentConfigMap = createNewConfigMap(baseName, len(configMaps), directory)
			currentSize = 0
		}

		currentConfigMap.BinaryData[relativePath] = content
		currentSize += len(content)

		return nil
	})

	if err != nil {
		return nil, err
	}

	configMaps = append(configMaps, *currentConfigMap)
	return configMaps, nil
}

func createNewConfigMap(baseName string, index int, directory string) *v1.ConfigMap {
	return &v1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ConfigMap",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-%d", baseName, index),
			Annotations: map[string]string{
				"gemini-source-directory": directory,
			},
		},
		BinaryData: make(map[string][]byte),
	}
}
