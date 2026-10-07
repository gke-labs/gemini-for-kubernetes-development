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
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/log"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/research"
)

// An apply, whichever recipe's: a member clicks one of the writes the
// task output stored for a run offers (Add labels, Post plan, Save to
// research/notes), and the controller runs factory apply --action on that
// output as it is then — edits included — with the clicker's token. The
// Request names the sandbox, the run and the action; success stamps the
// action applied in the run's applied annotation. What else a kind does
// once applied is its applyHook's.

// applyHook is what an apply of a run's output of one kind does beyond
// the write. Every field may be nil.
type applyHook struct {
	// spec is what the kind sets over the output's spec as it is applied.
	spec func(sb *unstructured.Unstructured) map[string]string
	// applied adjusts sb, once action is stamped applied there.
	applied func(sb *unstructured.Unstructured, action string) error
}

// applyHooks are by the kind of task output.
var applyHooks = map[string]applyHook{
	// A posted triage is finished with, so its sandbox is parked, unless a
	// plan or fix is running in it.
	"Triage": {
		applied: func(sb *unstructured.Unstructured, action string) error {
			if action != "comment" || sb.GetAnnotations()[factorycli.AnnotationTaskState] == factorycli.TaskStateRunning {
				return nil
			}
			return unstructured.SetNestedField(sb.Object, int64(0), "spec", "replicas")
		},
	},
	// Notes go under the file name the conversation pinned.
	"Notes": {
		spec: func(sb *unstructured.Unstructured) map[string]string {
			if name := sb.GetAnnotations()[research.NoteAnnotation]; name != "" {
				return map[string]string{"name": name}
			}
			return nil
		},
	},
}

// applyKey is the runner key of one write.
func applyKey(spec boardv1alpha1.RequestSpec) string {
	return fmt.Sprintf("%s/apply-%s-%s-%s", spec.Member, spec.Sandbox, spec.Apply.Run, spec.Apply.Action)
}

// applyMalformed is why an apply Request cannot be served as it is
// written, "" when it can.
func applyMalformed(spec boardv1alpha1.RequestSpec) string {
	switch {
	case spec.Apply == nil || spec.Sandbox == "" || spec.Apply.Run == "" || spec.Apply.Action == "":
		return "an apply names the sandbox, the run and the action"
	case !factorycli.IsApplyVerb(spec.Apply.Action):
		return fmt.Sprintf("%s is not a write factory apply makes", spec.Apply.Action)
	}
	return ""
}

// applyDraft is the sandbox an apply names, its run there and the output
// stored for it, when that output offers the action; nil when there is
// none. The sandbox is the member's, or the board's: auto-triage's, which
// shares its name.
func applyDraft(work *workState, spec boardv1alpha1.RequestSpec) (*unstructured.Unstructured, factorycli.RecordedRun, string) {
	runKey := factorycli.RunAnnotation(spec.Apply.Run)
	for _, ns := range []string{spec.Member, work.board.Namespace} {
		sb := work.findSandbox(ns, spec.Sandbox)
		if sb == nil {
			continue
		}
		a := sb.GetAnnotations()
		doc := a[factorycli.OutputAnnotation(runKey)]
		if doc == "" {
			continue
		}
		if !factorycli.Offers(factorycli.TaskOutputKind(doc), doc, spec.Apply.Action) {
			break
		}
		run, _ := factorycli.RecordedRunAt(a, runKey)
		return sb, run, doc
	}
	return nil, factorycli.RecordedRun{}, ""
}

// applyDoc is the task output to apply: doc, the one stored on sb, as it
// is now. One that names no task is given the run's — factory dedups its
// comments on it — or else the sandbox's, and one with no target, the
// sandbox's issue, else the repository.
func applyDoc(work *workState, sb *unstructured.Unstructured, run factorycli.RecordedRun, doc string) (string, error) {
	task := run.Task
	if task == "" {
		task = "board-" + sb.GetName()
	}
	url := sb.GetAnnotations()["htmlURL"]
	if url == "" {
		url = fmt.Sprintf("https://github.com/%s/%s", work.owner, work.repo)
	}
	var spec map[string]string
	if hook := applyHooks[factorycli.TaskOutputKind(doc)]; hook.spec != nil {
		spec = hook.spec(sb)
	}
	return factorycli.ApplyDoc(doc, task, url, spec)
}

// ensureApplies starts the write each apply Request asks for, once: not
// while it runs, and not again once it has a result — settle reads that.
// A controller that restarts mid-write loses the result and writes
// again, which factory makes harmless: labels add, and a comment carries
// its task's marker.
func (r *Reconciler) ensureApplies(ctx context.Context, work *workState, reqs []*boardv1alpha1.Request) {
	logger := log.FromContext(ctx)
	for _, req := range reqs {
		spec := req.Spec
		if applyMalformed(spec) != "" {
			continue
		}
		key := applyKey(spec)
		if r.Factory.IsRunning(key) {
			continue
		}
		if res, ok := r.Factory.LastResult(key); ok && res.FinishedAt.After(req.CreationTimestamp.Time) {
			continue
		}
		sb, run, stored := applyDraft(work, spec)
		if sb == nil {
			continue
		}
		doc, err := applyDoc(work, sb, run, stored)
		if err != nil {
			logger.Error(err, "unable to compose the task output", "sandbox", spec.Sandbox, "run", spec.Apply.Run)
			continue
		}
		token, err := r.executorToken(ctx, spec.Member)
		if err != nil {
			logger.Error(err, "apply executor has no token", "executor", spec.Member, "sandbox", spec.Sandbox)
			continue
		}
		if r.Factory.StartApply(key, factorycli.ApplyOptions{Doc: doc, Action: spec.Apply.Action, GithubToken: token}) {
			logger.Info("launched factory apply", "sandbox", spec.Sandbox, "run", spec.Apply.Run, "action", spec.Apply.Action, "member", spec.Member)
		}
	}
}

// settleApply is served by the write: the runner's result since the
// click. Success stamps the draft's sandbox, which is what the board reads
// the write as done from; the Request is only the receipt.
func (r *Reconciler) settleApply(ctx context.Context, work *workState, req *boardv1alpha1.Request, now time.Time) requestOutcome {
	spec := req.Spec
	if why := applyMalformed(spec); why != "" {
		return requestOutcome{phase: boardv1alpha1.RequestFailed, reason: "Malformed", message: why}
	}
	sb, _, _ := applyDraft(work, spec)
	key := applyKey(spec)
	res, ran := r.Factory.LastResult(key)
	ran = ran && res.FinishedAt.After(req.CreationTimestamp.Time)
	switch {
	case ran && res.Err != nil:
		return requestOutcome{
			phase:   boardv1alpha1.RequestFailed,
			reason:  "ApplyFailed",
			message: clipMessage(strings.TrimSpace(lastLines(res.Output, 3)+"\n"+res.Err.Error()), 400),
		}
	case ran:
		if sb == nil {
			// Written, and the draft went since (rejected): nothing left
			// to stamp.
			return requestOutcome{phase: boardv1alpha1.RequestSucceeded, reason: "Applied"}
		}
		if err := r.stampApplied(ctx, sb, spec); err != nil {
			log.FromContext(ctx).Error(err, "stamping the applied draft", "sandbox", sb.GetName())
			return stillPending
		}
		return requestOutcome{phase: boardv1alpha1.RequestSucceeded, reason: "Applied", sandbox: sb.GetName()}
	case r.Factory.IsRunning(key):
		return requestOutcome{phase: boardv1alpha1.RequestRunning}
	case sb == nil:
		return requestOutcome{
			phase:   boardv1alpha1.RequestFailed,
			reason:  "NoDraft",
			message: fmt.Sprintf("there is no %s draft on %s that offers %s", spec.Apply.Run, spec.Sandbox, spec.Apply.Action),
		}
	}
	return pendingOutcome(req, now)
}

// stampApplied marks the write done on the draft's sandbox, in its run's
// applied annotation, and does what the output's kind does once applied.
func (r *Reconciler) stampApplied(ctx context.Context, sb *unstructured.Unstructured, spec boardv1alpha1.RequestSpec) error {
	annotations := sb.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	runKey := factorycli.RunAnnotation(spec.Apply.Run)
	factorycli.MarkApplied(annotations, factorycli.AppliedAnnotation(runKey), spec.Apply.Action, time.Now())
	sb.SetAnnotations(annotations)
	if hook := applyHooks[factorycli.TaskOutputKind(annotations[factorycli.OutputAnnotation(runKey)])]; hook.applied != nil {
		if err := hook.applied(sb, spec.Apply.Action); err != nil {
			return err
		}
	}
	return r.Update(ctx, sb)
}

// lastLines is s's last n non-empty lines: what factory said last, for a
// failure's message.
func lastLines(s string, n int) string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
