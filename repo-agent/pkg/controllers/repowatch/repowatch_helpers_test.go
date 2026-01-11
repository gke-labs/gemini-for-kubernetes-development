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
	"fmt"
	"testing"
	"time"

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repowatch/v1alpha1"
	"github.com/google/go-github/v39/github"
	"github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCreateOrUpdateReviewSandboxes(t *testing.T) {
	g := gomega.NewWithT(t)
	s := runtime.NewScheme()
	_ = reviewv1alpha1.AddToScheme(s)

	repoWatch := &reviewv1alpha1.RepoWatch{
		ObjectMeta: metav1.ObjectMeta{Name: "test-watch", Namespace: "default", UID: types.UID("test-uid")},
		Spec: reviewv1alpha1.RepoWatchSpec{
			Review: reviewv1alpha1.PRReviewSpec{
				MaxActiveSandboxes:         1,
				MaxSandboxes:               2,
				ReviewShutdownAfterMinutes: 60,
			},
		},
	}

	// PR that should be created
	pr1Num := 1
	pr1 := &github.PullRequest{Number: &pr1Num, Head: &github.PullRequestBranch{Repo: &github.Repository{CloneURL: github.String("url")}, Ref: github.String("ref")}} // Add dummy data to avoid nil pointers

	// PR that should be pending (due to active limit)
	pr2Num := 2
	pr2 := &github.PullRequest{Number: &pr2Num, Head: &github.PullRequestBranch{Repo: &github.Repository{CloneURL: github.String("url")}, Ref: github.String("ref")}} // Add dummy data

	// PR that should be blocked (due to total limit)
	pr3Num := 3
	pr3 := &github.PullRequest{Number: &pr3Num, Head: &github.PullRequestBranch{Repo: &github.Repository{CloneURL: github.String("url")}, Ref: github.String("ref")}} // Add dummy data

	// PR with an existing, old sandbox that should be scaled down
	pr4Num := 4
	pr4 := &github.PullRequest{Number: &pr4Num}
	oldSandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "custom.agents.x-k8s.io/v1alpha1",
			"kind":       "ReviewSandbox",
			"metadata": map[string]interface{}{
				"name":              fmt.Sprintf("%s-pr-%d", repoWatch.Name, pr4Num),
				"namespace":         "default",
				"creationTimestamp": time.Now().Add(-61 * time.Minute).Format(time.RFC3339),
				"ownerReferences": []interface{}{
					map[string]interface{}{
						"uid": string(repoWatch.UID),
					},
				},
			},
			"spec": map[string]interface{}{"replicas": int64(1)},
		},
	}

	r := &Reconciler{
		Client: clientfake.NewClientBuilder().WithScheme(s).WithObjects(oldSandbox).Build(),
		Scheme: s,
	}

	// Start with one existing sandbox (oldSandbox)
	sandboxList := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*oldSandbox}}

	watched, pending, finalActive := r.reconcileReviewSandboxesInternal(context.Background(), repoWatch, []*github.PullRequest{}, []*github.PullRequest{pr1, pr2, pr3, pr4}, sandboxList)

	// Asserts
	g.Expect(finalActive).To(gomega.Equal(0), "Active count should be 0 because existing sandbox is scaled down and no new one created")
	g.Expect(len(watched)).To(gomega.Equal(1), "Should have only one existing watched PR")
	g.Expect(watched[0].Number).To(gomega.Equal(pr4Num))

	g.Expect(len(pending)).To(gomega.Equal(3), "Should have three pending PRs")
	g.Expect(pending[0]).To(gomega.Equal(pr1Num))
	g.Expect(pending[1]).To(gomega.Equal(pr2Num))
	g.Expect(pending[2]).To(gomega.Equal(pr3Num))

	// Verify scale down
	updatedSandbox := &unstructured.Unstructured{}
	updatedSandbox.SetGroupVersionKind(schema.GroupVersionKind{Group: "custom.agents.x-k8s.io", Version: "v1alpha1", Kind: "ReviewSandbox"})
	g.Expect(r.Client.Get(context.Background(), types.NamespacedName{Name: oldSandbox.GetName(), Namespace: "default"}, updatedSandbox)).To(gomega.Succeed())
	replicas, _, _ := unstructured.NestedInt64(updatedSandbox.Object, "spec", "replicas")
	g.Expect(replicas).To(gomega.Equal(int64(0)))
}

func TestCleanupClosedPRSandboxes(t *testing.T) {
	g := gomega.NewWithT(t)
	s := runtime.NewScheme()

	closedPRSandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "custom.agents.x-k8s.io/v1alpha1",
			"kind":       "ReviewSandbox",
			"metadata": map[string]interface{}{
				"name":      "test-repowatch-pr-2",
				"namespace": "default",
			},
		},
	}

	openPRNumber := 1
	openPR := &github.PullRequest{Number: &openPRNumber}

	r := &Reconciler{
		Client: clientfake.NewClientBuilder().WithScheme(s).WithObjects(closedPRSandbox).Build(),
		Scheme: s,
	}

	initialTotal := 1
	ownedSandboxes := []unstructured.Unstructured{*closedPRSandbox}
	allOpenPRs := []*github.PullRequest{openPR}

	finalTotal := r.cleanupClosedPRSandboxes(context.Background(), initialTotal, ownedSandboxes, allOpenPRs)

	g.Expect(finalTotal).To(gomega.Equal(0))

	sandboxList := &unstructured.UnstructuredList{}
	sandboxList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "custom.agents.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "ReviewSandbox",
	})
	g.Expect(r.Client.List(context.Background(), sandboxList)).To(gomega.Succeed())
	g.Expect(sandboxList.Items).To(gomega.HaveLen(0))
}

func TestCountSandboxes(t *testing.T) {
	g := gomega.NewWithT(t)

	explicitPRNumber := 1
	explicitPR := &github.PullRequest{Number: &explicitPRNumber}

	sandboxes := []unstructured.Unstructured{
		{
			Object: map[string]interface{}{
				"metadata": map[string]interface{}{
					"name": "sandbox-pr-1", // Explicit
				},
				"spec": map[string]interface{}{
					"replicas": int64(1),
				},
			},
		},
		{
			Object: map[string]interface{}{
				"metadata": map[string]interface{}{
					"name": "sandbox-pr-2", // Active
				},
				"spec": map[string]interface{}{
					"replicas": int64(1),
				},
			},
		},
		{
			Object: map[string]interface{}{
				"metadata": map[string]interface{}{
					"name": "sandbox-pr-3", // Inactive
				},
				"spec": map[string]interface{}{
					"replicas": int64(0),
				},
			},
		},
		{
			Object: map[string]interface{}{
				"metadata": map[string]interface{}{
					"name": "sandbox-pr-4", // Active
				},
				"spec": map[string]interface{}{
					"replicas": int64(1),
				},
			},
		},
	}

	active, total := countSandboxes(sandboxes, []*github.PullRequest{explicitPR})

	g.Expect(total).To(gomega.Equal(4))
	g.Expect(active).To(gomega.Equal(2)) // Should not include the explicit PR's sandbox
}

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
