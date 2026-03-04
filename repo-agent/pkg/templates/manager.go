// Copyright 2026 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package templates

import (
	"context"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

//go:embed data/*.yaml
var templatesFS embed.FS

const (
	TemplateLabel = "gemini-review-templates"
)

type Template struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Content     string `json:"content"` // Multi-document YAML
	Source      string `json:"source"`  // "system" or "user"
}

type Manager struct {
	clientset kubernetes.Interface
}

func NewManager(clientset kubernetes.Interface) *Manager {
	return &Manager{clientset: clientset}
}

func (m *Manager) List(ctx context.Context, namespace string) ([]Template, error) {
	var templates []Template

	// Helper to extract name from RepoWatch in YAML
	extractName := func(content string, defaultName string) string {
		decoder := yaml.NewDecoder(strings.NewReader(content))
		for {
			var node map[string]interface{}
			if err := decoder.Decode(&node); err != nil {
				if err == io.EOF {
					break
				}
				continue
			}

			// Check if Kind is RepoWatch
			if kind, ok := node["kind"].(string); ok && kind == "RepoWatch" {
				if metadata, ok := node["metadata"].(map[string]interface{}); ok {
					if name, ok := metadata["name"].(string); ok && name != "" && name != "change-name" {
						return name
					}
				}
			}
		}
		return defaultName
	}

	// 1. Load Embedded Templates (System)
	err := fs.WalkDir(templatesFS, "data", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".yaml" {
			return nil
		}

		content, err := templatesFS.ReadFile(path)
		if err != nil {
			return err
		}

		id := strings.TrimSuffix(filepath.Base(path), ".yaml")
		name := extractName(string(content), strings.ToUpper(id))

		templates = append(templates, Template{
			ID:          id,
			Name:        name,
			Description: "Built-in template",
			Content:     string(content),
			Source:      "system",
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	// 2. Load ConfigMap Templates (User)
	// List ConfigMaps with label
	cms, err := m.clientset.CoreV1().ConfigMaps(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=true", TemplateLabel),
	})
	// Ignore permission errors if any, just return system templates?
	// But if RBAC is set up correctly, this should work.
	if err == nil {
		for _, cm := range cms.Items {
			for filename, content := range cm.Data {
				if !strings.HasSuffix(filename, ".yaml") && !strings.HasSuffix(filename, ".yml") {
					continue
				}

				id := strings.TrimSuffix(filename, filepath.Ext(filename))
				name := extractName(content, cm.Name+" / "+id)

				templates = append(templates, Template{
					ID:          "custom-" + cm.Name + "-" + id,
					Name:        name,
					Description: "Custom template from ConfigMap",
					Content:     content,
					Source:      "user",
				})
			}
		}
	}

	// Sort by name
	sort.Slice(templates, func(i, j int) bool {
		return templates[i].Name < templates[j].Name
	})

	return templates, nil
}
