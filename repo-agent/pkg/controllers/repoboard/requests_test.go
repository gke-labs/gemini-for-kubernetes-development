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
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
)

// runClick is a Run-tab click: a mode and the name of one run.
func runClick(mode, name string) *boardv1alpha1.Request {
	return testRequest(boardv1alpha1.RequestSpec{
		Verb: boardv1alpha1.VerbRun,
		Run:  &boardv1alpha1.RunRequest{Mode: mode, Name: name},
	})
}

// runSandboxName is where the fixture board's run of that name lands.
func runSandboxName(name string) string {
	return factorycli.RunbookSandboxName("repo", factorycli.RunbookInstance(name, name))
}

// runSandbox is the sandbox a run shares across its modes, carrying the
// task stamps factory writes when a task finishes in it.
func runSandbox(name string, taskAt time.Time, state string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1alpha1",
		"kind":       "Sandbox",
		"metadata": map[string]interface{}{
			"name": runSandboxName(name), "namespace": "alice",
			"labels": map[string]interface{}{"factory.gemini.google.com/managed": "true"},
			"annotations": map[string]interface{}{
				"sandbox.gemini.google.com/last-task-time":  taskAt.UTC().Format(time.RFC3339),
				"sandbox.gemini.google.com/last-task-state": state,
			},
		},
		"spec": map[string]interface{}{"replicas": int64(1)},
	}}
}

// A run is the one click whose "already served" receipt is in memory:
// nothing on disk says a deploy happened until the runner reports. So
// the Request has to say it is spending BEFORE it spends — a process
// that dies in that window is exactly the case the phase exists for.
func TestARunClickSaysItIsLaunchingBeforeItSpends(t *testing.T) {
	g := gomega.NewWithT(t)
	req := runClick("deploy", "gcevm")
	fake := newFakeLauncher()
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), req)

	var phaseAtLaunch string
	fake.onStartRun = func(string) { phaseAtLaunch = requestStatus(t, r, req).Phase }

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.HaveLen(1))
	g.Expect(phaseAtLaunch).To(gomega.Equal(boardv1alpha1.RequestLaunching),
		"the click has to be written down as launching before the launch, not after it")

	// And once the runner holds the key, the click reads as in flight
	// rather than as one nobody picked up.
	status := requestStatus(t, r, req)
	g.Expect(status.Phase).To(gomega.Equal(boardv1alpha1.RequestRunning))
	g.Expect(status.Sandbox).To(gomega.Equal(runSandboxName("gcevm")))
	g.Expect(status.LaunchedAt).NotTo(gomega.BeNil())
}

// A controller killed mid-launch comes back with no memory of the
// deploy — but the pod outlived it, so the deploy may have happened.
// The click is stranded with a reason, never launched a second time:
// stranding a deploy costs a second click, repeating one costs a second
// cloud footprint.
func TestAnInterruptedRunIsStrandedNotRelaunched(t *testing.T) {
	g := gomega.NewWithT(t)
	req := runClick("deploy", "gcevm")
	launchedAt := metav1.NewTime(time.Now().Add(-10 * time.Minute))
	req.Status.Phase = boardv1alpha1.RequestLaunching
	req.Status.LaunchedAt = &launchedAt
	req.Status.Sandbox = runSandboxName("gcevm")

	// A fresh process: nothing running, no results, and a sandbox that
	// never reported a task.
	fake := newFakeLauncher()
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), req,
		runSandbox("gcevm", time.Now().Add(-time.Hour), factorycli.TaskStateCompleted))

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.BeEmpty(), "an interrupted deploy must not be repeated")

	status := requestStatus(t, r, req)
	g.Expect(status.Phase).To(gomega.Equal(boardv1alpha1.RequestFailed))
	g.Expect(status.Reason).To(gomega.Equal("LaunchInterrupted"))
	g.Expect(status.Message).NotTo(gomega.BeEmpty(), "the member reads the reason, not the phase")
}

// ...unless the sandbox watched it finish. The task stamp is the only
// thing that saw the run, so a stamp newer than the launch is the
// outcome — and the click reports it instead of a false alarm.
func TestAnInterruptedRunReadsItsOutcomeOffTheSandbox(t *testing.T) {
	for _, tc := range []struct {
		state string
		phase string
	}{
		{factorycli.TaskStateCompleted, boardv1alpha1.RequestSucceeded},
		{factorycli.TaskStateFailed, boardv1alpha1.RequestFailed},
	} {
		g := gomega.NewWithT(t)
		req := runClick("deploy", "gcevm")
		launchedAt := metav1.NewTime(time.Now().Add(-10 * time.Minute))
		req.Status.Phase = boardv1alpha1.RequestLaunching
		req.Status.LaunchedAt = &launchedAt

		fake := newFakeLauncher()
		r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), req,
			runSandbox("gcevm", time.Now().Add(-time.Minute), tc.state))

		_, err := r.Reconcile(context.Background(), boardRequest())
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(fake.launches()).To(gomega.BeEmpty())
		g.Expect(requestStatus(t, r, req).Phase).To(gomega.Equal(tc.phase), "task state %s", tc.state)
	}
}

// The window a restart actually lands in is the run itself, not the
// half-second of launching: the phase moves to Running within the same
// reconcile that launched. So the guard cannot be the phase. A click
// that says it was launched is never launched again, however long ago
// that was and whatever phase the last process left it in.
func TestARestartDuringARunDoesNotRelaunchIt(t *testing.T) {
	g := gomega.NewWithT(t)
	req := runClick("deploy", "gcevm")
	launchedAt := metav1.NewTime(time.Now().Add(-10 * time.Minute))
	req.Status.Phase = boardv1alpha1.RequestRunning
	req.Status.LaunchedAt = &launchedAt
	req.Status.Sandbox = runSandboxName("gcevm")

	// The pod outlived the controller and is still working: the stamp it
	// carries is the PREVIOUS task's, because last-task-time is written
	// when a task ends.
	fake := newFakeLauncher()
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), req,
		runSandbox("gcevm", time.Now().Add(-3*time.Hour), factorycli.TaskStateRunning))

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.BeEmpty(), "a deploy already in flight must not be started a second time")
	g.Expect(requestStatus(t, r, req).Phase).To(gomega.Equal(boardv1alpha1.RequestRunning),
		"a healthy long deploy must not be declared dead for outliving its controller")
}

// The sandbox is taken at its word, but not forever: a pod wedged in
// Running would otherwise hold a click open with no end.
func TestARunInFlightIsOnlyBelievedForSoLong(t *testing.T) {
	g := gomega.NewWithT(t)
	req := runClick("deploy", "gcevm")
	launchedAt := metav1.NewTime(time.Now().Add(-runInFlightGrace - time.Minute))
	req.Status.Phase = boardv1alpha1.RequestRunning
	req.Status.LaunchedAt = &launchedAt

	fake := newFakeLauncher()
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), req,
		runSandbox("gcevm", time.Now().Add(-4*time.Hour), factorycli.TaskStateRunning))

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.BeEmpty())

	status := requestStatus(t, r, req)
	g.Expect(status.Phase).To(gomega.Equal(boardv1alpha1.RequestFailed))
	g.Expect(status.Reason).To(gomega.Equal("LaunchInterrupted"))
}

// Saying "launching" before spending is only safe if the word is taken
// back when nothing was spent. A run refused because the sandbox is
// busy has not started: leave the stamp on and the click is stranded at
// the next pass, reporting an interruption that never happened.
func TestARefusedRunLaunchIsStillAClick(t *testing.T) {
	g := gomega.NewWithT(t)
	req := runClick("deploy", "gcevm")
	fake := newFakeLauncher()
	fake.refuseRun = true
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), req)

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.BeEmpty())

	status := requestStatus(t, r, req)
	g.Expect(status.Phase).To(gomega.Equal(boardv1alpha1.RequestPending), "a refusal is not a launch")
	g.Expect(status.LaunchedAt).To(gomega.BeNil())

	// And when the sandbox frees up, the same click launches.
	fake.refuseRun = false
	_, err = r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.HaveLen(1), "the click survived the refusal and was served")
}

// settled builds a terminal Request of a given age. Name and subject
// are the caller's because the whole point of the collection rules is
// which of several receipts for the same subject survive.
func settled(name, phase string, subject int, age time.Duration) *boardv1alpha1.Request {
	req := testRequest(boardv1alpha1.RequestSpec{Verb: boardv1alpha1.VerbTriage, Number: subject})
	req.Name = name
	at := metav1.NewTime(time.Now().Add(-age))
	req.CreationTimestamp = at
	req.Status.Phase = phase
	req.Status.CompletedAt = &at
	return req
}

// A settled click is a receipt, not history. It lingers long enough to
// be read and is then collected — successes quickly, failures for a
// week, because a failure is the only place the reason is written down
// and the member may not look until Monday.
func TestSettledRequestsAreCollectedOnTheirOwnClock(t *testing.T) {
	g := gomega.NewWithT(t)
	freshSuccess := settled("fresh-success", boardv1alpha1.RequestSucceeded, 1, 30*time.Minute)
	oldSuccess := settled("old-success", boardv1alpha1.RequestSucceeded, 2, 2*time.Hour)
	recentFailure := settled("recent-failure", boardv1alpha1.RequestFailed, 3, 48*time.Hour)
	oldFailure := settled("old-failure", boardv1alpha1.RequestFailed, 4, 8*24*time.Hour)

	fake := newFakeLauncher()
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(),
		freshSuccess, oldSuccess, recentFailure, oldFailure)

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	g.Expect(requestStatus(t, r, freshSuccess).Phase).To(gomega.Equal(boardv1alpha1.RequestSucceeded),
		"an hour is the point of the success TTL: the UI still has a row to stop showing")
	g.Expect(requestStatus(t, r, recentFailure).Phase).To(gomega.Equal(boardv1alpha1.RequestFailed),
		"a failure from the weekend has to survive to Monday")
	g.Expect(requestStatus(t, r, oldSuccess).Phase).To(gomega.BeEmpty(), "old-success should be gone")
	g.Expect(requestStatus(t, r, oldFailure).Phase).To(gomega.BeEmpty(), "old-failure should be gone")
}

// Clicking the same thing all afternoon leaves one receipt per click,
// and only the last of them is ever read. The cap is per subject so a
// busy issue cannot evict the only failure a quiet one ever had.
func TestRequestHistoryIsCappedPerSubject(t *testing.T) {
	g := gomega.NewWithT(t)
	var objs []runtime.Object
	objs = append(objs, testBoard(nil), githubSecret())
	// Newest last, all well inside the success TTL, so the cap is the
	// only thing that can remove any of them.
	for i := 0; i < maxHistory+3; i++ {
		age := time.Duration(maxHistory+3-i) * time.Minute
		objs = append(objs, settled(fmt.Sprintf("triage-30-%d", i), boardv1alpha1.RequestSucceeded, 30, age))
	}
	// A different subject with a single receipt: the cap must not reach
	// across subjects.
	quiet := settled("triage-31-only", boardv1alpha1.RequestFailed, 31, time.Minute)
	objs = append(objs, quiet)

	r := newTestReconciler(newFakeLauncher(), testGithubClient(`[]`), objs...)
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	kept := &boardv1alpha1.RequestList{}
	g.Expect(r.List(context.Background(), kept)).To(gomega.Succeed())
	surviving := map[string]bool{}
	for i := range kept.Items {
		surviving[kept.Items[i].Name] = true
	}
	g.Expect(surviving).To(gomega.HaveLen(maxHistory+1), "got %v", surviving)
	g.Expect(surviving["triage-31-only"]).To(gomega.BeTrue(), "the quiet subject's only receipt was evicted")
	// The survivors are the newest, which are the ones anyone might
	// still be looking for.
	for i := 0; i < 3; i++ {
		g.Expect(surviving[fmt.Sprintf("triage-30-%d", i)]).To(gomega.BeFalse(), "oldest %d should be gone", i)
	}
}

// A click nothing can serve fails with a reason. The annotation this
// replaced had no bound at all: an entry no pass understood sat on the
// board forever, and the member watched a row that would never fill.
func TestAClickNothingServesExpires(t *testing.T) {
	g := gomega.NewWithT(t)
	stale := click(boardv1alpha1.VerbTriage, 44)
	stale.CreationTimestamp = metav1.NewTime(time.Now().Add(-25 * time.Hour))

	r := newTestReconciler(newFakeLauncher(), testGithubClient(`[]`), testBoard(nil), githubSecret(), stale)
	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())

	status := requestStatus(t, r, stale)
	g.Expect(status.Phase).To(gomega.Equal(boardv1alpha1.RequestFailed))
	g.Expect(status.Reason).To(gomega.Equal("Expired"))
	g.Expect(status.CompletedAt).NotTo(gomega.BeNil())
}

// Two objects for the same click — two tabs racing the API's check —
// are one launch. The map the mailbox was gave this for free; an object
// per click has to say it.
func TestDuplicateClicksLaunchOnce(t *testing.T) {
	g := gomega.NewWithT(t)
	first := click(boardv1alpha1.VerbFix, 12)
	second := click(boardv1alpha1.VerbFix, 12)
	second.Name = first.Name + "-again"

	fake := newFakeLauncher()
	r := newTestReconciler(fake, testGithubClient(`[]`), testBoard(nil), githubSecret(), first, second)

	_, err := r.Reconcile(context.Background(), boardRequest())
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(fake.launches()).To(gomega.HaveLen(1), "a doubled click is one fix, not two")
}
