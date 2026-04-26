// Copyright 2026 The Gemini Authors.

package templates

import (
	"context"
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

	found := false
	for _, tmpl := range templates {
		if tmpl.ID == "gateway-api-reference-implementation" {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("Template 'gateway-api-reference-implementation' not found in %v", templates)
	}
}
