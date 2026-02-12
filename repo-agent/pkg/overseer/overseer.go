package overseer

import (
	"context"
	"fmt"
	"os"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repowatch/v1alpha1"
)

// Reconcile ensures the Overseer sandbox is running for the given RepoWatch.
func Reconcile(ctx context.Context, c client.Client, repoWatch *reviewv1alpha1.RepoWatch) error {
	log := log.FromContext(ctx)

	if repoWatch.Spec.Overseer == nil || !repoWatch.Spec.Overseer.Enabled {
		return nil
	}

	overseerName := fmt.Sprintf("overseer-%s", repoWatch.Name)

	// Define the sandbox object
	sandbox := &unstructured.Unstructured{}
	sandbox.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "agents.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "Sandbox",
	})

	err := c.Get(ctx, types.NamespacedName{Name: overseerName, Namespace: repoWatch.Namespace}, sandbox)
	if err != nil {
		if errors.IsNotFound(err) {
			// Create
			log.Info("Creating Overseer sandbox", "name", overseerName)
			newSandbox := newOverseerSandbox(repoWatch, overseerName)
			if err := controllerutil.SetControllerReference(repoWatch, newSandbox, c.Scheme()); err != nil {
				return err
			}
			return c.Create(ctx, newSandbox)
		}
		return err
	}

	repoWatch.Status.OverseerStatus = "Active"
	return nil
}

func newOverseerSandbox(repoWatch *reviewv1alpha1.RepoWatch, name string) *unstructured.Unstructured {
	// Construct the unstructured Sandbox

	image := repoWatch.Spec.Overseer.Image
	if image == "" {
		image = os.Getenv("OVERSEER_IMAGE")
	}

	apiKeySecretName := "gemini-api-key"
	if repoWatch.Spec.Review.LLM.APIKeySecretRef != "" {
		apiKeySecretName = repoWatch.Spec.Review.LLM.APIKeySecretRef
	} else if repoWatch.Spec.Issue != nil && repoWatch.Spec.Issue.LLM.APIKeySecretRef != "" {
		apiKeySecretName = repoWatch.Spec.Issue.LLM.APIKeySecretRef
	}

	githubSecretName := repoWatch.Spec.GithubSecretName

	// Pod Template Spec
	podSpec := map[string]interface{}{
		"serviceAccountName": "default", // TODO: Ensure this SA has permissions
		"containers": []interface{}{
			map[string]interface{}{
				"name":    "overseer",
				"image":   image,
				"command": []string{"/run.sh"},
				"env": []interface{}{
					map[string]interface{}{
						"name": "GITHUB_TOKEN",
						"valueFrom": map[string]interface{}{
							"secretKeyRef": map[string]interface{}{
								"name":     githubSecretName,
								"key":      "pat",
								"optional": true,
							},
						},
					},
					map[string]interface{}{
						"name": "GEMINI_API_KEY",
						"valueFrom": map[string]interface{}{
							"secretKeyRef": map[string]interface{}{
								"name":     apiKeySecretName,
								"key":      "key", // Assuming 'key' is the key in secret
								"optional": true,
							},
						},
					},
					map[string]interface{}{
						"name":  "REPO_URL",
						"value": repoWatch.Spec.RepoURL,
					},
				},
				"volumeMounts": []interface{}{
					// We might need to mount secrets if envFrom isn't enough or for other tools
					// For now, env vars should suffice for GITHUB_TOKEN and GEMINI_API_KEY
				},
			},
		},
	}

	u := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": repoWatch.Namespace,
				"labels": map[string]interface{}{
					"sandbox-type":                       "agent",
					"review.gemini.google.com/repowatch": repoWatch.Name,
				},
			},
			"spec": map[string]interface{}{
				"replicas": int64(1),
				"podTemplate": map[string]interface{}{
					"metadata": map[string]interface{}{
						"labels": map[string]interface{}{
							"sandbox":      name,
							"sandbox-type": "agent",
						},
					},
					"spec": podSpec,
				},
			},
		},
	}

	return u
}
