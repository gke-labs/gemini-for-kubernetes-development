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

package repoboard

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
)

// fixPRSandbox is alice's fix sandbox for issue 10, aliased to the PR it
// opened, 101.
func fixPRSandbox() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1alpha1",
		"kind":       "Sandbox",
		"metadata": map[string]interface{}{
			"name": "fix-repo-10", "namespace": "alice",
			"labels": map[string]interface{}{
				"factory.gemini.google.com/managed": "true",
				"factory.gemini.google.com/pr":      "101",
			},
			"annotations": map[string]interface{}{
				"sandbox.gemini.google.com/last-task-state": "Completed",
				"htmlURL": "https://github.com/test/repo/pull/101",
			},
		},
		"spec": map[string]interface{}{"replicas": int64(1)},
	}}
}

func watchClick(pr int) *boardv1alpha1.Request {
	return testRequest(boardv1alpha1.RequestSpec{Verb: boardv1alpha1.VerbWatch, Item: "pr", Number: pr})
}

func watchRequests(t *testing.T, r *Reconciler) []boardv1alpha1.Request {
	t.Helper()
	list := &boardv1alpha1.RequestList{}
	if err := r.List(context.Background(), list, client.InNamespace("alice"),
		client.MatchingLabels{boardv1alpha1.LabelVerb: boardv1alpha1.VerbWatch}); err != nil {
		t.Fatal(err)
	}
	return list.Items
}

// A fix's PR is watched from when the fix opens it, under the board's
// autoIterate policy: one watch Request, filed once, and the watch it
// keeps running cares for the PR as alice.
func TestFixPRIsWatched(t *testing.T) {
	g := gomega.NewWithT(t)
	fake := newFakeLauncher()
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), fixPRSandbox())

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	reqs := watchRequests(t, r)
	g.Expect(reqs).To(gomega.HaveLen(1))
	g.Expect(reqs[0].Spec.Member).To(gomega.Equal("alice"))
	g.Expect(reqs[0].Spec.Number).To(gomega.Equal(101))

	_, err = r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(watchRequests(t, r)).To(gomega.HaveLen(1), "filed once")
	var watches []fakeLaunch
	for _, l := range fake.launches() {
		if l.PRWatchOpts != nil {
			watches = append(watches, l)
		}
	}
	g.Expect(watches).To(gomega.HaveLen(1))
	g.Expect(watches[0].Key).To(gomega.Equal("alice/prwatch/test-board/101"))
	g.Expect(watches[0].PRWatchOpts.PRURL).To(gomega.Equal("https://github.com/test/repo/pull/101"))
	g.Expect(watches[0].PRWatchOpts.Namespace).To(gomega.Equal("alice"))

	sb := &unstructured.Unstructured{}
	sb.SetGroupVersionKind(sandboxGVK)
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "fix-repo-10", Namespace: "alice"}, sb)).To(gomega.Succeed())
	g.Expect(sb.GetAnnotations()[AnnotationWatchFiled]).To(gomega.Equal("101"))
}

// With the board's autoIterate off, a fix's PR starts with auto off, and
// turning the policy on later does not turn it on.
func TestFixPRNotWatchedWhenPolicyOff(t *testing.T) {
	g := gomega.NewWithT(t)
	board := testBoard(nil)
	off := false
	board.Spec.Policy.AutoIterate = &off
	r := newTestReconciler(newFakeLauncher(), testGithubClient(`[]`), board, githubSecret(), fixPRSandbox())
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(watchRequests(t, r)).To(gomega.BeEmpty())

	sb := &unstructured.Unstructured{}
	sb.SetGroupVersionKind(sandboxGVK)
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "fix-repo-10", Namespace: "alice"}, sb)).To(gomega.Succeed())
	g.Expect(sb.GetAnnotations()[AnnotationWatchFiled]).To(gomega.Equal("101"))
}

// Any PR of the member's is watched while its watch Request stands, fix
// or not; the Request stays Running, saying why its last watch failed.
func TestWatchRequestKeepsAWatchRunning(t *testing.T) {
	g := gomega.NewWithT(t)
	fake := newFakeLauncher()
	req := watchClick(55)
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), req)
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	launches := fake.launches()
	g.Expect(launches).To(gomega.HaveLen(1))
	g.Expect(launches[0].Key).To(gomega.Equal("alice/prwatch/test-board/55"))
	g.Expect(requestStatus(t, r, req).Phase).To(gomega.Equal(boardv1alpha1.RequestRunning))

	// The watch failed just now: not relaunched yet, and the Request says why.
	fake.results["alice/prwatch/test-board/55"] = factorycli.Result{Err: errors.New("exit status 1"), Output: "no token", FinishedAt: time.Now()}
	_, err = r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.HaveLen(1))
	st := requestStatus(t, r, req)
	g.Expect(st.Phase).To(gomega.Equal(boardv1alpha1.RequestRunning))
	g.Expect(st.Reason).To(gomega.Equal("WatchFailed"))
	g.Expect(st.Message).To(gomega.ContainSubstring("no token"))

	// Timed out long enough ago: relaunched.
	fake.results["alice/prwatch/test-board/55"] = factorycli.Result{FinishedAt: time.Now().Add(-prWatchRelaunchInterval - time.Minute)}
	_, err = r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.HaveLen(2))
}

// The PR merging settles its watch.
func TestWatchEndsWithThePR(t *testing.T) {
	g := gomega.NewWithT(t)
	fake := newFakeLauncher()
	req := watchClick(55)
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), req)
	fake.results["alice/prwatch/test-board/55"] = factorycli.Result{Output: "\nPR #55 is merged. Stopping watch.\n", FinishedAt: time.Now().Add(time.Second)}
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.BeEmpty())
	st := requestStatus(t, r, req)
	g.Expect(st.Phase).To(gomega.Equal(boardv1alpha1.RequestSucceeded))
	g.Expect(st.Reason).To(gomega.Equal("PRMerged"))
}

// Auto off deletes the Request; its running watch stops at once, and a
// fix's PR's watch is not filed again.
func TestAutoOffStopsTheWatch(t *testing.T) {
	g := gomega.NewWithT(t)
	fake := newFakeLauncher()
	sb := fixPRSandbox()
	annotations := sb.GetAnnotations()
	annotations[AnnotationWatchFiled] = "101"
	sb.SetAnnotations(annotations)
	fake.running["alice/prwatch/test-board/101"] = true
	fake.running["alice/prwatch/other-board/101"] = true
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), sb)
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.stopped).To(gomega.Equal([]string{"alice/prwatch/test-board/101"}))
	g.Expect(watchRequests(t, r)).To(gomega.BeEmpty())
}
