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

package controllers

import (
	"context"
	"errors"
	"strconv"
	"strings"

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/repowatch/api/v1alpha1"
	"github.com/google/go-github/v39/github"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// cleanupClosedPRSandboxes iterates through owned sandboxes and deletes those whose corresponding PRs are closed.
// It returns the updated count of total sandboxes.
func (r *RepoWatchReconciler) cleanupClosedPRSandboxes(ctx context.Context, totalSandboxes int, ownedSandboxes []unstructured.Unstructured, allOpenPRs []*github.PullRequest) int {
	log := log.FromContext(ctx)
	for _, sandbox := range ownedSandboxes {
		prNumber, err := strconv.Atoi(strings.Split(sandbox.GetName(), "-pr-")[1])
		if err != nil {
			log.Error(err, "unable to parse pr number from sandbox name", "sandbox", sandbox.GetName())
			continue
		}

		found := false
		for _, pr := range allOpenPRs {
			if *pr.Number == prNumber {
				found = true
				break
			}
		}

		if !found {
			log.Info("deleting sandbox for closed pr", "pr", prNumber)
			if err := r.Delete(ctx, &sandbox); err != nil {
				log.Error(err, "unable to delete sandbox", "sandbox", sandbox.GetName())
			} else {
				totalSandboxes--
			}
		}
	}
	return totalSandboxes
}

// countSandboxes calculates the number of active and total sandboxes from a given slice of owned sandboxes.
// Sandboxes for explicit PRs are not counted towards the active limit.
func countSandboxes(ownedSandboxes []unstructured.Unstructured, explicitPRs []*github.PullRequest) (int, int) {
	activeSandboxes := 0
	totalSandboxes := len(ownedSandboxes)
	for _, sandbox := range ownedSandboxes {
		replicas, found, err := unstructured.NestedInt64(sandbox.Object, "spec", "replicas")
		if err == nil && found && replicas > 0 {
			// Check if the PR is explicit, if so, dont count it towards the active sandbox limit.
			// An "explicit" PR is one that is specifically listed in the `RepoWatch`
			// spec's `pullRequests` field.
			var prIsExplicit bool
			prNumber, err := strconv.Atoi(strings.Split(sandbox.GetName(), "-pr-")[1])
			if err == nil {
				prIsExplicit = isPRExplicit(prNumber, explicitPRs)
			}
			if !prIsExplicit {
				activeSandboxes++
			}
		}
	}
	return activeSandboxes, totalSandboxes
}

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

func (r *RepoWatchReconciler) sortPRs(ctx context.Context, prs []*github.PullRequest, repoWatch *reviewv1alpha1.RepoWatch, user *github.User) []*github.PullRequest {
	log := log.FromContext(ctx)
	// Sort by PreferAssignedToSelf
	// TODO(barney-s): May be rate limited. Cache the user info.
	if repoWatch.Spec.Review.PreferAssignedToSelf {
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
	return prs
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
