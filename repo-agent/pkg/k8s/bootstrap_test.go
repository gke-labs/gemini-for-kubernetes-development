// Copyright 2026 The Gemini Authors.

package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestBootstrapNamespace(t *testing.T) {
	clientset := fake.NewSimpleClientset(
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      GithubSecretName,
				Namespace: SystemNamespace,
			},
			Data: map[string][]byte{"pat": []byte("dummy-pat")},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "github-portal-ca",
				Namespace: "overseer-system",
			},
			Data: map[string][]byte{"ca.crt": []byte("dummy-ca")},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      GeminiSecretName,
				Namespace: SystemNamespace,
			},
			Data: map[string][]byte{"token": []byte("dummy-token")},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      ClaudeSecretName,
				Namespace: SystemNamespace,
			},
			Data: map[string][]byte{"key": []byte("dummy-key")},
		},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      DevContainerCM,
				Namespace: SystemNamespace,
			},
			Data: map[string]string{"devcontainer.json": "{}"},
		},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "registry",
				Namespace: "repo-agent-system",
			},
			Spec: corev1.ServiceSpec{
				ClusterIP: "10.96.0.5",
				Ports: []corev1.ServicePort{
					{
						Port: 5000,
					},
				},
			},
		},
	)

	targetNS := "user-alice"
	ctx := context.Background()

	err := BootstrapNamespace(ctx, clientset, targetNS)
	if err != nil {
		t.Fatalf("BootstrapNamespace failed: %v", err)
	}

	// Verify namespace is created
	_, err = clientset.CoreV1().Namespaces().Get(ctx, targetNS, metav1.GetOptions{})
	if err != nil {
		t.Errorf("Expected namespace %s to be created, got error: %v", targetNS, err)
	}

	// Verify secrets and configs are copied
	_, err = clientset.CoreV1().Secrets(targetNS).Get(ctx, GithubSecretName, metav1.GetOptions{})
	if err != nil {
		t.Errorf("Expected secret %s to be copied to namespace %s, got error: %v", GithubSecretName, targetNS, err)
	}

	// Verify the NetworkPolicy is created
	policy, err := clientset.NetworkingV1().NetworkPolicies(targetNS).Get(ctx, "sandbox-egress-policy", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Expected NetworkPolicy sandbox-egress-policy to be created in namespace %s, got error: %v", targetNS, err)
	}

	// Check matching expressions on NetworkPolicy
	if len(policy.Spec.PodSelector.MatchExpressions) != 1 {
		t.Errorf("Expected 1 match expression, got %d", len(policy.Spec.PodSelector.MatchExpressions))
	} else {
		req := policy.Spec.PodSelector.MatchExpressions[0]
		if req.Key != "sandbox" || req.Operator != metav1.LabelSelectorOpExists {
			t.Errorf("Unexpected MatchExpression: %v", req)
		}
	}

	// Verify that registry egress rule is present
	foundRegistryRule := false
	for _, rule := range policy.Spec.Egress {
		for _, peer := range rule.To {
			if peer.IPBlock != nil && peer.IPBlock.CIDR == "10.96.0.5/32" {
				if len(rule.Ports) == 1 && rule.Ports[0].Port.IntVal == 5000 {
					foundRegistryRule = true
				}
			}
		}
	}
	if !foundRegistryRule {
		t.Errorf("Expected to find an egress rule allowing access to registry (10.96.0.5/32 on port 5000), but none was found. Policy: %+v", policy.Spec)
	}
}

func TestBootstrapNamespaceSimple(t *testing.T) {
	clientset := fake.NewSimpleClientset(
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "github-portal-ca",
				Namespace: "overseer-system",
			},
			Data: map[string][]byte{"ca.crt": []byte("dummy-ca")},
		},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      DevContainerCM,
				Namespace: SystemNamespace,
			},
			Data: map[string]string{"devcontainer.json": "{}"},
		},
	)

	targetNS := "user-bob"
	ctx := context.Background()

	err := BootstrapNamespaceSimple(ctx, clientset, targetNS)
	if err != nil {
		t.Fatalf("BootstrapNamespaceSimple failed: %v", err)
	}

	// Verify namespace is created
	_, err = clientset.CoreV1().Namespaces().Get(ctx, targetNS, metav1.GetOptions{})
	if err != nil {
		t.Errorf("Expected namespace %s to be created, got error: %v", targetNS, err)
	}

	// Verify the NetworkPolicy is created
	_, err = clientset.NetworkingV1().NetworkPolicies(targetNS).Get(ctx, "sandbox-egress-policy", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Expected NetworkPolicy sandbox-egress-policy to be created in namespace %s, got error: %v", targetNS, err)
	}
}
