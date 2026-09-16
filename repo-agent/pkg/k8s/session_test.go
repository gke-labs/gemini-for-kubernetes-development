package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// A fresh cluster gets a generated secret that is stable across restarts.
func TestEnsureSessionSecret_GeneratesOnceAndPersists(t *testing.T) {
	clientset := fake.NewSimpleClientset()

	first, err := EnsureSessionSecret(context.Background(), clientset)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if first == "" {
		t.Fatal("expected a non-empty generated secret")
	}

	second, err := EnsureSessionSecret(context.Background(), clientset)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if second != first {
		t.Fatalf("secret not stable across calls: %q vs %q", first, second)
	}

	stored, err := clientset.CoreV1().Secrets(SystemNamespace).Get(context.Background(), sessionSecretName, v1.GetOptions{})
	if err != nil {
		t.Fatalf("stored secret: %v", err)
	}
	if string(stored.Data[sessionSecretKey]) != first {
		t.Fatal("persisted secret does not match returned value")
	}
}

// An existing secret is reused verbatim.
func TestEnsureSessionSecret_UsesExisting(t *testing.T) {
	clientset := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: v1.ObjectMeta{Name: sessionSecretName, Namespace: SystemNamespace},
		Data:       map[string][]byte{sessionSecretKey: []byte("preexisting")},
	})

	got, err := EnsureSessionSecret(context.Background(), clientset)
	if err != nil {
		t.Fatalf("EnsureSessionSecret: %v", err)
	}
	if got != "preexisting" {
		t.Fatalf("expected preexisting secret, got %q", got)
	}
}
