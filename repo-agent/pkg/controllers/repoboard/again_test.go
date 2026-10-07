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

package repoboard

import (
	"testing"
	"time"

	"github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
)

// A launch click after the recipe's last run on the row ended is the
// recipe again: a new run, for triage, plan and fix as for any recipe.
// A click from before that run is the run it asked for, and is served.
func TestLaunchAgainStartsANewRun(t *testing.T) {
	hourAgo := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	for _, tc := range []struct {
		recipe      string
		annotations map[string]interface{}
		key         string
	}{
		{"fix", map[string]interface{}{
			"sandbox.gemini.google.com/last-task-type":  "fix",
			"sandbox.gemini.google.com/last-task-state": "Failed",
			"sandbox.gemini.google.com/completion-time": hourAgo,
		}, "alice/fix-repo-10"},
		{"plan", map[string]interface{}{
			"sandbox.gemini.google.com/last-task-type":  "plan",
			"sandbox.gemini.google.com/last-task-state": "Completed",
			"board.gemini.google.com/planned-at":        hourAgo,
			"board.gemini.google.com/plan-output":       "kind: Plan\nspec:\n  markdown: old\n",
		}, "alice/plan-repo-10"},
		{"triage", map[string]interface{}{
			"sandbox.gemini.google.com/recipe-triage-task-state": "Completed",
			"board.gemini.google.com/triaged-at":                 hourAgo,
		}, "alice/triage-repo-10"},
	} {
		sandbox := func() *unstructured.Unstructured {
			a := map[string]interface{}{"htmlURL": "https://github.com/test/repo/issues/10"}
			for k, v := range tc.annotations {
				a[k] = v
			}
			return &unstructured.Unstructured{Object: map[string]interface{}{
				"apiVersion": "agents.x-k8s.io/v1alpha1",
				"kind":       "Sandbox",
				"metadata": map[string]interface{}{
					"name": "fix-repo-10", "namespace": "alice",
					"labels":      map[string]interface{}{"factory.gemini.google.com/managed": "true"},
					"annotations": a,
				},
				"spec": map[string]interface{}{"replicas": int64(0)},
			}}
		}
		t.Run(tc.recipe+" again", func(t *testing.T) {
			g := gomega.NewWithT(t)
			fake := newFakeLauncher()
			req := launch(tc.recipe, 10)
			req.UID = "uid-again"
			r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), sandbox(), req)
			r.reconcileBoard(t)
			launches := fake.launches()
			g.Expect(launches).To(gomega.HaveLen(1))
			g.Expect(launches[0].Key).To(gomega.Equal(tc.key))
			g.Expect(requestStatus(t, r, req).Phase).NotTo(gomega.Equal(boardv1alpha1.RequestSucceeded))
		})
		t.Run(tc.recipe+" before the run", func(t *testing.T) {
			g := gomega.NewWithT(t)
			fake := newFakeLauncher()
			req := launch(tc.recipe, 10)
			req.CreationTimestamp = metav1.NewTime(time.Now().Add(-2 * time.Hour))
			r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), sandbox(), req)
			r.reconcileBoard(t)
			g.Expect(fake.launches()).To(gomega.BeEmpty())
			g.Expect(requestStatus(t, r, req).Phase).To(gomega.Equal(boardv1alpha1.RequestSucceeded))
		})
	}
}
