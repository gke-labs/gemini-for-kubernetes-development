/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package api

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
)

// PR reviews are executed by the factory CLI since the factory migration:
// the repowatch controller launches `factory pr review` and stores the draft
// on the factory sandbox under the legacy agentDraft/userDraft annotation
// names. These helpers resolve that sandbox for the PR-review endpoints.
const (
	labelFactoryPR      = "factory.gemini.google.com/pr"
	labelRepoWatch      = "review.gemini.google.com/repowatch"
	annoTaskState       = "sandbox.gemini.google.com/last-task-state"
	annoCompletionTime  = "sandbox.gemini.google.com/completion-time"
	annoRereviewRequest = "review.gemini.google.com/rereview-requested-at"
)

// resolveFactoryPRSandbox finds the factory-managed sandbox working on a PR.
// Factory review sandbox names are not repo-qualified, so when one namespace
// watches several repos the repowatch decoration label disambiguates.
func (s *Server) resolveFactoryPRSandbox(ctx context.Context, namespace, repo, prID string) (*unstructured.Unstructured, error) {
	list, err := s.K8sManager.ListSandboxes(ctx, namespace, labelFactoryPR+"="+prID)
	if err != nil {
		return nil, err
	}
	var fallback *unstructured.Unstructured
	for i := range list.Items {
		sb := &list.Items[i]
		if sb.GetLabels()[labelRepoWatch] == repo {
			return sb, nil
		}
		if fallback == nil {
			fallback = sb
		}
	}
	if fallback != nil {
		return fallback, nil
	}
	return nil, fmt.Errorf("no factory sandbox found for PR %s", prID)
}

// factoryReviewTask synthesizes the single UI task entry for a factory-run
// review from the sandbox's annotations (the SandboxTask CR no longer exists
// for reviews).
func factoryReviewTask(sb *unstructured.Unstructured) models.Task {
	annotations := sb.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}

	state := annotations[annoTaskState]
	switch {
	case state == "" && annotations["agentDraft"] != "":
		state = "Completed"
	case state == "":
		state = "Pending"
	}

	createdAt := sb.GetCreationTimestamp().Format(time.RFC3339)
	if completion := annotations[annoCompletionTime]; completion != "" {
		createdAt = completion
	}

	return models.Task{
		Name:              sb.GetName(),
		Type:              "review",
		TaskState:         state,
		CreationTimestamp: createdAt,
		AgentDraft:        annotations["agentDraft"],
		UserDraft:         annotations["userDraft"],
		AgentState:        annotations["agentState"],
		AgentStateMessage: annotations["agentStateMessage"],
	}
}
