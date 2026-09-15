/*
Copyright 2025.

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

package repowatch

import (
	"context"
	"errors"
	"strings"

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repowatch/v1alpha1"
	"github.com/google/go-github/v39/github"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// getOwnedSandboxes filters a slice of sandboxes and returns only those owned by the specified UID.
func getOwnedSandboxes(sandboxes []unstructured.Unstructured, ownerUID types.UID) []unstructured.Unstructured {
	var ownedSandboxes []unstructured.Unstructured
	for _, sandbox := range sandboxes {
		isOwned := false
		for _, ownerRef := range sandbox.GetOwnerReferences() {
			if ownerRef.UID == ownerUID {
				isOwned = true
				break
			}
		}
		if isOwned {
			ownedSandboxes = append(ownedSandboxes, sandbox)
		}
	}
	return ownedSandboxes
}

// getOwnedIssueSandboxes filters a slice of sandboxes and returns only those owned by the specified UID and handler name.
func getOwnedIssueSandboxes(sandboxes []unstructured.Unstructured, ownerUID types.UID, handlerName string) []unstructured.Unstructured {
	var ownedSandboxes []unstructured.Unstructured
	for _, sandbox := range sandboxes {
		isOwned := false
		for _, ownerRef := range sandbox.GetOwnerReferences() {
			if ownerRef.UID == ownerUID {
				isOwned = true
				break
			}
		}
		if !isOwned {
			continue
		}

		// Further filter by handler name encoded in the sandbox name
		parts := strings.Split(sandbox.GetName(), "-issue-")
		if len(parts) < 2 {
			continue
		}
		handlerParts := strings.SplitN(parts[1], "-", 2)
		if len(handlerParts) < 2 {
			continue
		}
		sandboxHandlerName := handlerParts[1]
		if sandboxHandlerName == handlerName {
			ownedSandboxes = append(ownedSandboxes, sandbox)
		}
	}
	return ownedSandboxes
}

func (r *Reconciler) sortPRs(ctx context.Context, prs []*github.PullRequest, _ *reviewv1alpha1.RepoWatch, user *github.User) []*github.PullRequest {
	// Prioritize PRs assigned to the current user
	log := log.FromContext(ctx)
	if user == nil || user.Login == nil {
		log.Error(errors.New("user or user login is nil"), "unable to get current user login for sorting PRs")
		return prs
	}
	var assignedToMe []*github.PullRequest
	var others []*github.PullRequest
	for _, pr := range prs {
		isAssigned := false
		for _, assignee := range pr.Assignees {
			if assignee.Login != nil && *assignee.Login == *user.Login {
				isAssigned = true
				break
			}
		}
		if isAssigned {
			assignedToMe = append(assignedToMe, pr)
		} else {
			others = append(others, pr)
		}
	}
	return append(assignedToMe, others...)
}

// isPRExplicit checks if a given PR number is in the list of explicit PRs.
// An "explicit" PR is one that is specifically listed in the `RepoWatch`
// spec's `pullRequests` field.
func isPRExplicit(prNumber int, explicitPRs []*github.PullRequest) bool {
	for _, explicitPR := range explicitPRs {
		if *explicitPR.Number == prNumber {
			return true
		}
	}
	return false
}

func isIssueExplicit(issueNumber int, explicitIssues []int) bool {
	for _, explicitIssue := range explicitIssues {
		if explicitIssue == issueNumber {
			return true
		}
	}
	return false
}
