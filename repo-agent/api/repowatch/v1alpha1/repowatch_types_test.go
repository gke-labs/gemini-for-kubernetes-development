package v1alpha1

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRepoWatchTypes(t *testing.T) {
	// Create a RepoWatch object with the new LLMConfig
	repoWatch := &RepoWatch{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-repowatch",
			Namespace: "test-namespace",
		},
		Spec: RepoWatchSpec{
			RepoURL: "https://github.com/test/repo",
			Review: PRReviewSpec{
				LLM: LLMConfig{
					Provider:        GeminiProvider,
					APIKeySecretRef: "test-secret",
					Prompt:          "test-prompt",
					ConfigdirRef:    "test-configdir",
				},
			},
		},
	}

	// Verify that the fields are set correctly
	expectedLLMConfig := LLMConfig{
		Provider:        GeminiProvider,
		APIKeySecretRef: "test-secret",
		Prompt:          "test-prompt",
		ConfigdirRef:    "test-configdir",
	}

	if diff := cmp.Diff(expectedLLMConfig, repoWatch.Spec.Review.LLM); diff != "" {
		t.Errorf("RepoWatch.Spec.Review.LLM mismatch (-want +got):\n%s", diff)
	}
}
