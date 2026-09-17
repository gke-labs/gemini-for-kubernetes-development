package k8s

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	sessionSecretName = "session-secret"
	sessionSecretKey  = "secret"
)

// EnsureSessionSecret returns a stable cookie-encryption secret, persisting a
// generated one in the system namespace on first use. Without this, every API
// restart would mint a new random secret and invalidate all user sessions.
func EnsureSessionSecret(ctx context.Context, clientset kubernetes.Interface) (string, error) {
	existing, err := clientset.CoreV1().Secrets(SystemNamespace).Get(ctx, sessionSecretName, v1.GetOptions{})
	if err == nil {
		if v, ok := existing.Data[sessionSecretKey]; ok && len(v) > 0 {
			return string(v), nil
		}
	} else if !errors.IsNotFound(err) {
		return "", fmt.Errorf("reading secret %s/%s: %w", SystemNamespace, sessionSecretName, err)
	}

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating session secret: %w", err)
	}
	generated := base64.StdEncoding.EncodeToString(b)

	secret := &corev1.Secret{
		ObjectMeta: v1.ObjectMeta{
			Name:      sessionSecretName,
			Namespace: SystemNamespace,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "repo-agent"},
		},
		Data: map[string][]byte{sessionSecretKey: []byte(generated)},
	}
	if _, err := clientset.CoreV1().Secrets(SystemNamespace).Create(ctx, secret, v1.CreateOptions{}); err != nil {
		if errors.IsAlreadyExists(err) {
			// Another replica won the race; use its secret.
			winner, getErr := clientset.CoreV1().Secrets(SystemNamespace).Get(ctx, sessionSecretName, v1.GetOptions{})
			if getErr != nil {
				return "", fmt.Errorf("reading concurrently created secret %s/%s: %w", SystemNamespace, sessionSecretName, getErr)
			}
			if v, ok := winner.Data[sessionSecretKey]; ok && len(v) > 0 {
				return string(v), nil
			}
			return "", fmt.Errorf("secret %s/%s has no %s key", SystemNamespace, sessionSecretName, sessionSecretKey)
		}
		return "", fmt.Errorf("creating secret %s/%s: %w", SystemNamespace, sessionSecretName, err)
	}
	return generated, nil
}
