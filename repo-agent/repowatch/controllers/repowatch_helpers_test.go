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
	"io"
	"net/http"
	"strings"
	"testing"

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/repowatch/api/v1alpha1"
	"github.com/google/go-github/v39/github"
	"github.com/onsi/gomega"
)

func TestFilterPRsByLabels(t *testing.T) {
	g := gomega.NewWithT(t)

	// Helper to create PR with labels
	createPR := func(number int, labels ...string) *github.PullRequest {
		prLabels := []*github.Label{}
		for _, l := range labels {
			lStr := l // copy for pointer
			prLabels = append(prLabels, &github.Label{Name: &lStr})
		}
		return &github.PullRequest{
			Number: &number,
			Labels: prLabels,
		}
	}

	tests := []struct {
		name           string
		repoWatchSpec  reviewv1alpha1.RepoWatchSpec
		inputPRs       []*github.PullRequest
		expectedPRNums []int
	}{
		{
			name: "No labels filter",
			repoWatchSpec: reviewv1alpha1.RepoWatchSpec{
				Review: reviewv1alpha1.PRReviewSpec{
					Labels: [][]string{},
				},
			},
			inputPRs: []*github.PullRequest{
				createPR(1, "bug"),
				createPR(2),
			},
			expectedPRNums: []int{1, 2},
		},
		{
			name: "Single label required",
			repoWatchSpec: reviewv1alpha1.RepoWatchSpec{
				Review: reviewv1alpha1.PRReviewSpec{
					Labels: [][]string{{"bug"}},
				},
			},
			inputPRs: []*github.PullRequest{
				createPR(1, "bug"),
				createPR(2, "enhancement"),
				createPR(3, "bug", "critical"),
			},
			expectedPRNums: []int{1, 3},
		},
		{
			name: "Multiple label sets (OR logic)",
			repoWatchSpec: reviewv1alpha1.RepoWatchSpec{
				Review: reviewv1alpha1.PRReviewSpec{
					Labels: [][]string{{"bug"}, {"security"}},
				},
			},
			inputPRs: []*github.PullRequest{
				createPR(1, "bug"),
				createPR(2, "enhancement"),
				createPR(3, "security"),
				createPR(4, "documentation"),
			},
			expectedPRNums: []int{1, 3},
		},
		{
			name: "Composite label set (AND logic)",
			repoWatchSpec: reviewv1alpha1.RepoWatchSpec{
				Review: reviewv1alpha1.PRReviewSpec{
					Labels: [][]string{{"bug", "critical"}},
				},
			},
			inputPRs: []*github.PullRequest{
				createPR(1, "bug"),
				createPR(2, "bug", "critical"),
				createPR(3, "critical"),
			},
			expectedPRNums: []int{2},
		},
	}

	r := &RepoWatchReconciler{}

	for _, tc := range tests {
		t.Run(tc.name, func(_ *testing.T) {
			repoWatch := &reviewv1alpha1.RepoWatch{Spec: tc.repoWatchSpec}
			filtered := r.filterPRsByLabels(tc.inputPRs, repoWatch)

			g.Expect(len(filtered)).To(gomega.Equal(len(tc.expectedPRNums)))
			for i, pr := range filtered {
				g.Expect(*pr.Number).To(gomega.Equal(tc.expectedPRNums[i]))
			}
		})
	}
}

func TestDeduplicatePRs(t *testing.T) {
	g := gomega.NewWithT(t)

	createPR := func(number int) *github.PullRequest {
		return &github.PullRequest{Number: &number}
	}

	tests := []struct {
		name           string
		prs            []*github.PullRequest
		explicitPRs    []*github.PullRequest
		expectedPRNums []int
	}{
		{
			name: "No duplicates",
			prs: []*github.PullRequest{
				createPR(1),
				createPR(2),
			},
			explicitPRs: []*github.PullRequest{
				createPR(3),
			},
			expectedPRNums: []int{1, 2},
		},
		{
			name: "With duplicates",
			prs: []*github.PullRequest{
				createPR(1),
				createPR(2),
				createPR(3),
			},
			explicitPRs: []*github.PullRequest{
				createPR(2),
			},
			expectedPRNums: []int{1, 3},
		},
		{
			name: "All duplicates",
			prs: []*github.PullRequest{
				createPR(1),
			},
			explicitPRs: []*github.PullRequest{
				createPR(1),
			},
			expectedPRNums: []int{},
		},
	}

	r := &RepoWatchReconciler{}

	for _, tc := range tests {
		t.Run(tc.name, func(_ *testing.T) {
			deduped := r.deduplicatePRs(tc.prs, tc.explicitPRs)

			g.Expect(len(deduped)).To(gomega.Equal(len(tc.expectedPRNums)))
			for i, pr := range deduped {
				g.Expect(*pr.Number).To(gomega.Equal(tc.expectedPRNums[i]))
			}
		})
	}
}

func TestSortPRs(t *testing.T) {
	g := gomega.NewWithT(t)

	createPRWithAssignee := func(number int, assignees ...string) *github.PullRequest {
		var ghAssignees []*github.User
		for _, a := range assignees {
			aStr := a
			ghAssignees = append(ghAssignees, &github.User{Login: &aStr})
		}
		return &github.PullRequest{
			Number:    &number,
			Assignees: ghAssignees,
		}
	}

	tests := []struct {
		name          string
		preferSelf    bool
		inputPRs      []*github.PullRequest
		expectedOrder []int
	}{
		{
			name:       "Sort disabled",
			preferSelf: false,
			inputPRs: []*github.PullRequest{
				createPRWithAssignee(1, "other"),
				createPRWithAssignee(2, "myself"),
			},
			expectedOrder: []int{1, 2},
		},
		{
			name:       "Sort enabled",
			preferSelf: true,
			inputPRs: []*github.PullRequest{
				createPRWithAssignee(1, "other"),
				createPRWithAssignee(2, "myself"),
				createPRWithAssignee(3, "someone-else"),
			},
			expectedOrder: []int{2, 1, 3},
		},
		{
			name:       "Sort enabled - mixed",
			preferSelf: true,
			inputPRs: []*github.PullRequest{
				createPRWithAssignee(1, "other"),
				createPRWithAssignee(2, "myself", "other"), // assigned to me and others
				createPRWithAssignee(3, "myself"),
			},
			expectedOrder: []int{2, 3, 1}, // 2 and 3 are assigned to me, order between them is stable (relative to input)
		},
	}

	r := &RepoWatchReconciler{}

	for _, tc := range tests {
		t.Run(tc.name, func(_ *testing.T) {
			mockHTTPClient := &http.Client{
				Transport: &mockRoundTripper{
					responses: map[string]func() *http.Response{
						"https://api.github.com/user": func() *http.Response {
							return &http.Response{
								StatusCode: http.StatusOK,
								Body:       io.NopCloser(strings.NewReader(`{"login": "myself", "name": "Me"}`)),
							}
						},
					},
				},
			}
			ghClient := github.NewClient(mockHTTPClient)

			repoWatch := &reviewv1alpha1.RepoWatch{
				Spec: reviewv1alpha1.RepoWatchSpec{
					Review: reviewv1alpha1.PRReviewSpec{
						PreferAssignedToSelf: tc.preferSelf,
					},
				},
			}

			user := &github.User{Login: github.String("myself")}
			sorted := r.sortPRs(context.Background(), tc.inputPRs, repoWatch, user)

			g.Expect(len(sorted)).To(gomega.Equal(len(tc.expectedOrder)))
			for i, pr := range sorted {
				g.Expect(*pr.Number).To(gomega.Equal(tc.expectedOrder[i]))
			}
		})
	}
}
