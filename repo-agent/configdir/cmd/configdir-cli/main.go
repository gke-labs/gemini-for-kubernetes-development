/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	configdirv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/configdir/api/v1alpha1"
)

func main() {
	var name, namespace, directory string
	var syncToCluster, ignoreNotFoundError, includeFolderName bool
	flag.StringVar(&name, "name", "", "The name of the ConfigDir resource. If empty directory name is used.")
	flag.StringVar(&namespace, "namespace", "default", "The namespace of the ConfigDir.")
	flag.StringVar(&directory, "directory", "", "The directory to sync the files from or to.")
	flag.BoolVar(&syncToCluster, "sync-to-cluster", false, "Sync from filesystem to cluster.")
	flag.BoolVar(&includeFolderName, "include-folder-name", false, "includes the last item(folder) of the path passed to --directory parameter")
	flag.BoolVar(&ignoreNotFoundError, "ignore-not-found-error", false, "ignores not found errors during sync.")
	flag.Parse()

	cfg, err := config.GetConfig()
	if err != nil {
		log.Fatalf("unable to get kubeconfig: %v", err)
	}

	if err := configdirv1alpha1.AddToScheme(scheme.Scheme); err != nil {
		log.Fatalf("unable to add scheme: %v", err)
	}

	cli, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		log.Fatalf("unable to create kubernetes client: %v", err)
	}

	ctx := context.Background()
	if directory == "" {
		log.Fatalf("--directory is required when --sync-to-cluster is set.")
	}
	if name == "" {
		log.Print("--name is not set, using directory name as ConfigDir name")
		name = filepath.Base(directory)
	}
	if syncToCluster {
		if err := syncConfigDataToCluster(ctx, cli, directory, includeFolderName, name, namespace); err != nil {
			log.Fatalf("failed: %v", err)
		}
		log.Print("successfully synced to cluster")
		return
	}

	if err := os.MkdirAll(directory, 0755); err != nil {
		log.Fatalf("unable to create target directory: %v", err)
	}

	configDir := &configdirv1alpha1.ConfigDir{}
	if err := cli.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, configDir); err != nil {
		if client.IgnoreNotFound(err) == nil && ignoreNotFoundError {
			log.Printf("ConfigDir %s not found in namespace %s, but ignoring not found error as requested.", name, namespace)
			return
		}
		log.Fatalf("unable to fetch ConfigDir: %v", err)
	}

	for _, file := range configDir.Spec.Files {
		var content []byte
		var err error

		source := file.Source
		switch {
		case source.Inline != "":
			content = []byte(source.Inline)
		case source.ConfigMapRef != nil:
			cm := &corev1.ConfigMap{}
			if err := cli.Get(ctx, types.NamespacedName{Name: source.ConfigMapRef.Name, Namespace: namespace}, cm); err != nil {
				log.Printf("unable to fetch ConfigMap %s: %v", source.ConfigMapRef.Name, err)
				continue
			}
			content = []byte(cm.Data[source.ConfigMapRef.Key])
		case source.SecretRef != nil:
			secret := &corev1.Secret{}
			if err := cli.Get(ctx, types.NamespacedName{Name: source.SecretRef.Name, Namespace: namespace}, secret); err != nil {
				log.Printf("unable to fetch Secret %s: %v", source.SecretRef.Name, err)
				continue
			}
			content = secret.Data[source.SecretRef.Key]
		case source.URL != nil:
			content, err = fetchURL(ctx, cli, namespace, source.URL)
			if err != nil {
				log.Printf("unable to fetch URL %s: %v", source.URL.Location, err)
				continue
			}
		case source.FileContentKey != "":
			content, err = findFileContent(ctx, cli, namespace, configDir.Spec.FileContentSelector, source.FileContentKey)
			if err != nil {
				log.Printf("unable to find file content key %s: %v", source.FileContentKey, err)
				continue
			}
		}

		filePath := filepath.Join(directory, file.Path)
		if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
			log.Printf("unable to create directory for %s: %v", filePath, err)
			continue
		}
		if err := os.WriteFile(filePath, content, 0644); err != nil {
			log.Printf("unable to write file: %s: %v", filePath, err)
			continue
		}
		log.Printf("synced file: %s", filePath)
	}
}

func syncConfigDataToCluster(ctx context.Context, cli client.Client, sourceDir string, includeFolderName bool, configDirName, namespace string) error {
	type fileInfo struct {
		path    string // relative path
		content []byte
		size    int64
	}
	var files []fileInfo
	var totalSize int64

	relPathPrefix := ""
	if includeFolderName {
		relPathPrefix = filepath.Base(sourceDir)
	}
	err := filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			relPath, err := filepath.Rel(sourceDir, path)
			relPath = filepath.Join(relPathPrefix, relPath)
			if err != nil {
				return err
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			size := info.Size()
			files = append(files, fileInfo{path: relPath, content: content, size: size})
			totalSize += size
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to walk directory: %w", err)
	}

	log.Printf("found files. count: %d, totalSize: %d", len(files), totalSize)

	configDir := &configdirv1alpha1.ConfigDir{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configDirName,
			Namespace: namespace,
		},
		Spec: configdirv1alpha1.ConfigDirSpec{},
	}

	const oneMB = 1 * 1024 * 1024
	if totalSize < oneMB {
		log.Print("total size is less than 1MB, using inline files")
		for _, f := range files {
			configDir.Spec.Files = append(configDir.Spec.Files, configdirv1alpha1.FileItem{
				Path: f.path,
				Source: configdirv1alpha1.FileSource{
					Inline: string(f.content),
				},
			})
		}
	} else {
		log.Print("total size is >= 1MB, using ConfigMaps for files")
		for _, f := range files {
			if f.size > oneMB {
				return fmt.Errorf("file %s is larger than 1MB and cannot be stored in a ConfigMap", f.path)
			}

			cmName := fmt.Sprintf("%s-%s", configDirName, safeConfigMapName(f.path))
			cm := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      cmName,
					Namespace: namespace,
				},
				Data: map[string]string{
					filepath.Base(f.path): string(f.content),
				},
			}

			var existingCm corev1.ConfigMap
			err := cli.Get(ctx, types.NamespacedName{Name: cmName, Namespace: namespace}, &existingCm)
			if err != nil {
				if client.IgnoreNotFound(err) != nil {
					return fmt.Errorf("failed to get configmap %s: %w", cmName, err)
				}
				if err := cli.Create(ctx, cm); err != nil {
					return fmt.Errorf("failed to create configmap %s: %w", cmName, err)
				}
				log.Printf("created configmap %s", cmName)
			} else {
				existingCm.Data = cm.Data
				if err := cli.Update(ctx, &existingCm); err != nil {
					return fmt.Errorf("failed to update configmap %s: %w", cmName, err)
				}
				log.Printf("updated configmap %s", cmName)
			}

			configDir.Spec.Files = append(configDir.Spec.Files, configdirv1alpha1.FileItem{
				Path: f.path,
				Source: configdirv1alpha1.FileSource{
					ConfigMapRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: cmName,
						},
						Key: filepath.Base(f.path),
					},
				},
			})
		}
	}

	var existingCd configdirv1alpha1.ConfigDir
	err = cli.Get(ctx, types.NamespacedName{Name: configDirName, Namespace: namespace}, &existingCd)
	if err != nil {
		if client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("failed to get configdir %s: %w", configDirName, err)
		}
		if err := cli.Create(ctx, configDir); err != nil {
			return fmt.Errorf("failed to create configdir %s: %w", configDirName, err)
		}
		log.Printf("created configdir %s", configDirName)
	} else {
		existingCd.Spec = configDir.Spec
		if err := cli.Update(ctx, &existingCd); err != nil {
			return fmt.Errorf("failed to update configdir %s: %w", configDirName, err)
		}
		log.Printf("updated configdir %s", configDirName)
	}

	return nil
}

func safeConfigMapName(filePath string) string {
	h := sha256.New()
	h.Write([]byte(filePath))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func findFileContent(ctx context.Context, cli client.Client, namespace string, selector *metav1.LabelSelector, key string) ([]byte, error) {
	// Search in ConfigMaps
	cmList := &corev1.ConfigMapList{}
	if err := cli.List(ctx, cmList, client.InNamespace(namespace), client.MatchingLabels(selector.MatchLabels)); err != nil {
		return nil, err
	}
	for _, cm := range cmList.Items {
		if val, ok := cm.Data[key]; ok {
			return []byte(val), nil
		}
		if val, ok := cm.BinaryData[key]; ok {
			return val, nil
		}
	}

	// Search in ConfigFiles
	cfList := &configdirv1alpha1.ConfigFileList{}
	if err := cli.List(ctx, cfList, client.InNamespace(namespace), client.MatchingLabels(selector.MatchLabels)); err != nil {
		return nil, err
	}
	for _, cf := range cfList.Items {
		for _, file := range cf.Spec.Files {
			if file.Path == key {
				// content is base64 encoded
				return base64.StdEncoding.DecodeString(file.Content)
			}
		}
	}

	return nil, fmt.Errorf("key %s not found in any matching ConfigMap or ConfigFile", key)
}

func fetchURL(ctx context.Context, cli client.Client, namespace string, urlSource *configdirv1alpha1.URLSource) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", urlSource.Location, nil)
	if err != nil {
		return nil, err
	}

	if urlSource.SecretRef != nil {
		secret := &corev1.Secret{}
		if err := cli.Get(ctx, types.NamespacedName{Name: urlSource.SecretRef.Name, Namespace: namespace}, secret); err != nil {
			return nil, fmt.Errorf("unable to fetch secret for URL auth: %w", err)
		}
		token, ok := secret.Data[urlSource.SecretRef.Key]
		if !ok {
			return nil, fmt.Errorf("key %s not found in secret %s", urlSource.SecretRef.Key, urlSource.SecretRef.Name)
		}
		req.Header.Set("Authorization", string(token))
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	content, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if urlSource.SHA256 != "" {
		hasher := sha256.New()
		hasher.Write(content)
		hash := hex.EncodeToString(hasher.Sum(nil))
		if hash != urlSource.SHA256 {
			return nil, fmt.Errorf("sha256 mismatch for %s", urlSource.Location)
		}
	}

	return content, nil
}
