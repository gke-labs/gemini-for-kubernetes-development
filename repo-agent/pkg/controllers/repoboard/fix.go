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
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/log"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
)

// The fix (factory/design/fix-recipe.md): `factory recipe fix` in the
// issue's sandbox commits, and pushes to the member's fork; the runner
// opens the branch as a draft PR (open-pr) when the run ends. The PR's
// follow-ups (Iterate, Address comments, Fix CI) are revises of the fix,
// asked into its session: each pushes to the same branch, and the runner
// posts the replies and report it wrote on the PR (post-replies). The
// run, and every revise of it, is recorded under fix-run.

// fixKey is the runner key of an issue's fixes. PR/issue numbers repeat
// across repos, so it carries the repo: two boards in one namespace must
// never share a single-flight slot.
func fixKey(work *workState, member string, issue int) string {
	return fmt.Sprintf("%s/fix-%s-%d", member, work.repo, issue)
}

// fixRunName is what a fix's task is recorded under in its sandbox, as
// reviewRunName is for a review's.
func fixRunName(board string, issue int) string {
	return fmt.Sprintf("%s%d", fixRunPrefix(board, issue), time.Now().Unix())
}

func fixRunPrefix(board string, issue int) string {
	return fmt.Sprintf("fix/%s/%d/", board, issue)
}

// fixRunUnread is the fix run recorded on the sandbox whose result the
// controller has not read: started after the last one it read, and after
// any Fix again. A revise of the fix, recorded under the same key, is not
// the fix's.
func fixRunUnread(annotations map[string]string, board string, issue int) (string, bool) {
	var since time.Time
	for _, key := range []string{AnnotationFixHarvestedAt, AnnotationRefixRequested} {
		if at, err := time.Parse(time.RFC3339, annotations[key]); err == nil && at.After(since) {
			since = at
		}
	}
	name, ok := factorycli.RecordedRunName(annotations, factorycli.AnnotationFixRun, since)
	if !ok || !strings.HasPrefix(name, fixRunPrefix(board, issue)) {
		return "", false
	}
	return name, true
}

// recordFixResult writes down, on the sandbox, that the controller read
// the fix's newest result: when, and why it failed if it did. The PR it
// opened is on the sandbox already (open-pr aliased it).
func (r *Reconciler) recordFixResult(ctx context.Context, sb *unstructured.Unstructured, key string) {
	res, ok := r.Factory.LastResult(key)
	if !ok {
		return
	}
	annotations := sb.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	if at, err := time.Parse(time.RFC3339, annotations[AnnotationFixHarvestedAt]); err == nil && !res.FinishedAt.Truncate(time.Second).After(at) {
		return
	}
	annotations[AnnotationFixHarvestedAt] = res.FinishedAt.UTC().Format(time.RFC3339)
	if res.Err != nil {
		annotations[AnnotationFixError] = fixErrorLine(res)
	} else {
		delete(annotations, AnnotationFixError)
	}
	sb.SetAnnotations(annotations)
	if err := r.Update(ctx, sb); err != nil {
		log.FromContext(ctx).Error(err, "unable to record the fix's result", "sandbox", sb.GetName())
	}
}

// fixErrorLine is the most useful line of a failed fix's output: factory
// prints "Error: ..." on its way out.
func fixErrorLine(res factorycli.Result) string {
	lines := strings.Split(strings.TrimSpace(res.Output), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); strings.HasPrefix(line, "Error:") {
			return clipMessage(strings.TrimSpace(strings.TrimPrefix(line, "Error:")), 300)
		}
	}
	return clipMessage(res.Err.Error(), 300)
}

// fixRevise reports whether a sandbox-keyed revise names an issue's
// sandbox with a fix run: a follow-up of the fix.
func fixRevise(work *workState, spec boardv1alpha1.RequestSpec) bool {
	sb := work.findSandbox(spec.Member, spec.Sandbox)
	if _, ok := factorycli.IssueOf(sb, work.repo); !ok {
		return false
	}
	return sb.GetAnnotations()[factorycli.AnnotationFixRun] != ""
}

// ensureFixRevise starts a fix's follow-up, as ensureReviewRevise starts
// an Update review: not while the fix itself runs.
func (r *Reconciler) ensureFixRevise(ctx context.Context, work *workState, req *boardv1alpha1.Request) {
	logger := log.FromContext(ctx)
	spec := req.Spec
	sb := work.findSandbox(spec.Member, spec.Sandbox)
	issue, ok := factorycli.IssueOf(sb, work.repo)
	if !ok {
		return
	}
	key := sandboxReviseKey(spec.Member, spec.Sandbox)
	if r.Factory.IsRunning(key) || r.Factory.IsRunning(fixKey(work, spec.Member, issue)) {
		return
	}
	if res, ok := r.Factory.LastResult(key); ok && res.FinishedAt.After(req.CreationTimestamp.Time) {
		return
	}
	annotations := sb.GetAnnotations()
	session := factorycli.RecordedRunSession(annotations, factorycli.AnnotationFixRun)
	if session == "" {
		return
	}
	token, err := r.executorToken(ctx, spec.Member)
	if err != nil {
		logger.Error(err, "revise executor has no token", "executor", spec.Member, "sandbox", spec.Sandbox)
		return
	}
	prefix := sandboxRevisePrefix(work.board.Name, spec.Sandbox, spec.Revise)
	runName := fmt.Sprintf("%s%d", prefix, time.Now().Unix())
	if name, ok := r.resumableReviseRun(key, annotations, factorycli.AnnotationFixRun, prefix, req); ok {
		runName = name
	}
	var inputs map[string]string
	if spec.Instruction != "" {
		inputs = map[string]string{"instruction": spec.Instruction}
	}
	if err := r.wake(ctx, sb); err != nil {
		logger.Error(err, "unable to wake the sandbox for a revise", "sandbox", sb.GetName())
		return
	}
	if r.Factory.StartRevise(key, factorycli.ReviseOptions{
		Namespace:   spec.Member,
		SandboxName: sb.GetName(),
		Revise:      spec.Revise,
		Session:     session,
		GithubToken: token,
		RunName:     runName,
		// A follow-up is an agent turn and a push, not a rewrite of a
		// draft: give it the time a fix's turn takes.
		Timeout:     45 * time.Minute,
		PostReplies: true,
		Inputs:      inputs,
	}) {
		logger.Info("launched factory recipe revise", "sandbox", sb.GetName(), "revise", spec.Revise, "member", spec.Member)
	}
}

// settleFixRevise is served by the follow-up's result since the click:
// pushed, with its replies posted, or its failure.
func (r *Reconciler) settleFixRevise(work *workState, req *boardv1alpha1.Request, now time.Time) requestOutcome {
	spec := req.Spec
	key := sandboxReviseKey(spec.Member, spec.Sandbox)
	res, ran := r.Factory.LastResult(key)
	ran = ran && res.FinishedAt.After(req.CreationTimestamp.Time)
	switch {
	case ran && res.Err != nil:
		return requestOutcome{
			phase:   boardv1alpha1.RequestFailed,
			reason:  "ReviseFailed",
			message: clipMessage(strings.TrimSpace(lastLines(res.Output, 3)+"\n"+res.Err.Error()), 400),
		}
	case ran:
		return requestOutcome{phase: boardv1alpha1.RequestSucceeded, reason: "Revised", sandbox: spec.Sandbox}
	case r.Factory.IsRunning(key):
		return requestOutcome{phase: boardv1alpha1.RequestRunning}
	}
	return pendingOutcome(req, now)
}
