package overseer

import (
	"context"
	"fmt"
	"strings"

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
		Group:   "custom.agents.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "IssueSandbox",
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

	// For now, we don't update the sandbox if it exists, to avoid restarting it unnecessarily.
	// If the spec changes (e.g. image), we might want to update it.
	// But since IssueSandbox is immutable-ish (Pod based), update might require delete/recreate.
	// Let's keep it simple: create if missing.

	repoWatch.Status.OverseerStatus = "Active"
	return nil // Status update is handled by caller (Reconcile loop)
}

func newOverseerSandbox(repoWatch *reviewv1alpha1.RepoWatch, name string) *unstructured.Unstructured {
	// Construct the unstructured IssueSandbox
	// mirroring how dev_sandbox.go does it but for 'agent' type

	cloneURL := strings.Replace(repoWatch.Spec.RepoURL, "github.com", "github.com", 1)
	if !strings.HasSuffix(cloneURL, ".git") {
		cloneURL += ".git"
	}

	// We default to "gemini-api-key" if not specified in other parts,
	// but ideally this should be configurable.
	apiKeySecretName := "gemini-api-key"
	if repoWatch.Spec.Review.LLM.APIKeySecretRef != "" {
		apiKeySecretName = repoWatch.Spec.Review.LLM.APIKeySecretRef
	} else if repoWatch.Spec.Issue != nil && repoWatch.Spec.Issue.LLM.APIKeySecretRef != "" {
		apiKeySecretName = repoWatch.Spec.Issue.LLM.APIKeySecretRef
	}

	u := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "custom.agents.x-k8s.io/v1alpha1",
			"kind":       "IssueSandbox",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": repoWatch.Namespace,
				"labels": map[string]interface{}{
					"sandbox.gemini.google.com/type":     "agent", // Specific type for Overseer
					"review.gemini.google.com/repowatch": repoWatch.Name,
				},
				"annotations": map[string]interface{}{
					"agentState": "provisioning",
				},
			},
			"spec": map[string]interface{}{
				"source": map[string]interface{}{
					"cloneURL": cloneURL,
					"htmlURL":  repoWatch.Spec.RepoURL,
				},
				"destination": map[string]interface{}{
					"branch": "main", // Default branch
				},
				"githubSecretName": repoWatch.Spec.GithubSecretName,
				"image":            repoWatch.Spec.Overseer.Image,
				"replicas":         int64(1),
				"command":          []string{"/run.sh"}, // Explicitly run the loop script
				"llm": map[string]interface{}{
					"apiKeySecretName": apiKeySecretName,
					"prompt":           "You are the Overseer.", // Placeholder, prompt is in image
				},
			},
		},
	}

	return u
}
