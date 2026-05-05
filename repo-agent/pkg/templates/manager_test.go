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
	"strings"
	"testing"

	"k8s.io/client-go/kubernetes/fake"
)

func TestManagerList(t *testing.T) {
	clientset := fake.NewClientset()
	m := NewManager(clientset)

	templates, err := m.List(context.Background(), "default")
	if err != nil {
		t.Fatalf("Failed to list templates: %v", err)
	}

	if len(templates) == 0 {
		t.Errorf("Expected at least one template, got 0")
	}

	foundGateway := false
	for _, tmpl := range templates {
		if tmpl.ID == "gateway-api-reference-implementation" {
			foundGateway = true
		}
	}

	if !foundGateway {
		t.Errorf("Template 'gateway-api-reference-implementation' not found in %v", templates)
	}
}

func TestManagerEmbeddedTemplates(t *testing.T) {
	clientset := fake.NewClientset()
	m := NewManager(clientset)

	templates, err := m.List(context.Background(), "default")
	if err != nil {
		t.Fatalf("Failed to list templates: %v", err)
	}

	// We expect at least the 8 templates from main + ai-factory = 9
	expectedCount := 9
	if len(templates) < expectedCount {
		t.Errorf("Expected at least %d templates, got %d. Templates: %v", expectedCount, len(templates), templates)
	}

	for _, tmpl := range templates {
		if tmpl.Source != "system" {
			continue
		}
		if tmpl.Name == "" || strings.HasPrefix(tmpl.Name, "SYSTEM") {
			t.Errorf("Template %s has invalid name: %q", tmpl.ID, tmpl.Name)
		}
	}
}
