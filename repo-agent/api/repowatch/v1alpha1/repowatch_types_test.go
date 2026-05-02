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
