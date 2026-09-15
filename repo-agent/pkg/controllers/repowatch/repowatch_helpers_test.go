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
	"testing"

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repowatch/v1alpha1"
	"github.com/google/go-github/v39/github"
	"github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

func TestGetOwnedIssueSandboxes_Comprehensive(t *testing.T) {
	ownerUID := types.UID("test-uid")
	otherUID := types.UID("other-uid")
	handlerName := "test-handler-with-hyphens"

	testCases := []struct {
		name          string
		sandboxes     []unstructured.Unstructured
		expectedCount int
		expectedNames []string
	}{
		{
			name: "happy path with single match",
			sandboxes: []unstructured.Unstructured{
				{
					Object: map[string]interface{}{
						"metadata": map[string]interface{}{
							"name": "repo-issue-1-" + handlerName,
							"ownerReferences": []interface{}{
								map[string]interface{}{"uid": string(ownerUID)},
							},
						},
					},
				},
			},
			expectedCount: 1,
			expectedNames: []string{"repo-issue-1-" + handlerName},
		},
		{
			name: "multiple matches",
			sandboxes: []unstructured.Unstructured{
				{
					Object: map[string]interface{}{
						"metadata": map[string]interface{}{
							"name": "repo-issue-1-" + handlerName,
							"ownerReferences": []interface{}{
								map[string]interface{}{"uid": string(ownerUID)},
							},
						},
					},
				},
				{
					Object: map[string]interface{}{
						"metadata": map[string]interface{}{
							"name": "repo-issue-2-" + handlerName,
							"ownerReferences": []interface{}{
								map[string]interface{}{"uid": string(ownerUID)},
							},
						},
					},
				},
			},
			expectedCount: 2,
			expectedNames: []string{"repo-issue-1-" + handlerName, "repo-issue-2-" + handlerName},
		},
		{
			name: "no match due to wrong handler",
			sandboxes: []unstructured.Unstructured{
				{
					Object: map[string]interface{}{
						"metadata": map[string]interface{}{
							"name": "repo-issue-1-wrong-handler",
							"ownerReferences": []interface{}{
								map[string]interface{}{"uid": string(ownerUID)},
							},
						},
					},
				},
			},
			expectedCount: 0,
		},
		{
			name: "no match due to wrong owner",
			sandboxes: []unstructured.Unstructured{
				{
					Object: map[string]interface{}{
						"metadata": map[string]interface{}{
							"name": "repo-issue-1-" + handlerName,
							"ownerReferences": []interface{}{
								map[string]interface{}{"uid": string(otherUID)},
							},
						},
					},
				},
			},
			expectedCount: 0,
		},
		{
			name: "malformed name - no issue separator",
			sandboxes: []unstructured.Unstructured{
				{
					Object: map[string]interface{}{
						"metadata": map[string]interface{}{
							"name": "repo-1-" + handlerName,
							"ownerReferences": []interface{}{
								map[string]interface{}{"uid": string(ownerUID)},
							},
						},
					},
				},
			},
			expectedCount: 0,
		},
		{
			name: "malformed name - no handler part",
			sandboxes: []unstructured.Unstructured{
				{
					Object: map[string]interface{}{
						"metadata": map[string]interface{}{
							"name": "repo-issue-1",
							"ownerReferences": []interface{}{
								map[string]interface{}{"uid": string(ownerUID)},
							},
						},
					},
				},
			},
			expectedCount: 0,
		},
		{
			name:          "empty input slice",
			sandboxes:     []unstructured.Unstructured{},
			expectedCount: 0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			g := gomega.NewWithT(t)
			ownedSandboxes := getOwnedIssueSandboxes(tc.sandboxes, ownerUID, handlerName)
			g.Expect(len(ownedSandboxes)).To(gomega.Equal(tc.expectedCount))

			if tc.expectedCount > 0 {
				var foundNames []string
				for _, sb := range ownedSandboxes {
					foundNames = append(foundNames, sb.GetName())
				}
				g.Expect(foundNames).To(gomega.ConsistOf(tc.expectedNames))
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
		inputPRs      []*github.PullRequest
		expectedOrder []int
	}{
		{
			name: "Sorts by assignment",
			inputPRs: []*github.PullRequest{
				createPRWithAssignee(1, "other"),
				createPRWithAssignee(2, "myself"),
			},
			expectedOrder: []int{2, 1},
		},
		{
			name: "Sorts by assignment - complex",
			inputPRs: []*github.PullRequest{
				createPRWithAssignee(1, "other"),
				createPRWithAssignee(2, "myself"),
				createPRWithAssignee(3, "someone-else"),
			},
			expectedOrder: []int{2, 1, 3},
		},
		{
			name: "Sorts by assignment - mixed",
			inputPRs: []*github.PullRequest{
				createPRWithAssignee(1, "other"),
				createPRWithAssignee(2, "myself", "other"), // assigned to me and others
				createPRWithAssignee(3, "myself"),
			},
			expectedOrder: []int{2, 3, 1}, // 2 and 3 are assigned to me, order between them is stable (relative to input)
		},
	}

	r := &Reconciler{}

	for _, tc := range tests {
		t.Run(tc.name, func(_ *testing.T) {
			repoWatch := &reviewv1alpha1.RepoWatch{
				Spec: reviewv1alpha1.RepoWatchSpec{
					Review: reviewv1alpha1.PRReviewSpec{},
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

func TestIsPRExplicit(t *testing.T) {
	g := gomega.NewWithT(t)

	pr1Num := 1
	pr1 := &github.PullRequest{Number: &pr1Num}
	pr2Num := 2
	pr2 := &github.PullRequest{Number: &pr2Num}

	explicitPRs := []*github.PullRequest{pr1}

	g.Expect(isPRExplicit(*pr1.Number, explicitPRs)).To(gomega.BeTrue())
	g.Expect(isPRExplicit(*pr2.Number, explicitPRs)).To(gomega.BeFalse())
}
