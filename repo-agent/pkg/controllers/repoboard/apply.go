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
)

// applyWrites are the writes an apply Request may ask for, by kind, and
// the annotation each stamps on the draft's sandbox once it is done.
var applyWrites = map[string]map[string]string{
	"Triage": {
		"label":   factorycli.AnnotationTriageLabeled,
		"comment": AnnotationTriagePublished,
	},
	"Plan": {
		"comment": factorycli.AnnotationPlanCommented,
	},
	"Notes": {
		"push-notes": AnnotationNotesSaved,
	},
}

// applyStamp is the annotation an apply's write stamps, or "" for a write
// nothing serves.
func applyStamp(spec boardv1alpha1.RequestSpec) string {
	if spec.Apply == nil {
		return ""
	}
	return applyWrites[spec.Apply.Kind][spec.Apply.Action]
}

// applyKey is the runner key of one write.
func applyKey(work *workState, spec boardv1alpha1.RequestSpec) string {
	if spec.Sandbox != "" {
		return fmt.Sprintf("%s/apply-%s-%s-%s", spec.Member, spec.Sandbox, strings.ToLower(spec.Apply.Kind), spec.Apply.Action)
	}
	return fmt.Sprintf("%s/apply-%s-%d-%s-%s", spec.Member, work.repo, spec.Number, strings.ToLower(spec.Apply.Kind), spec.Apply.Action)
}

// applyDraftSandbox is the sandbox holding the draft an apply writes: for
// a triage, the member's, else the board's, where auto-triage runs; for a
// plan, the member's issue sandbox; for notes, the research sandbox the
// Request names.
func applyDraftSandbox(work *workState, spec boardv1alpha1.RequestSpec) *unstructured.Unstructured {
	switch spec.Apply.Kind {
	case "Triage":
		for _, ns := range []string{spec.Member, work.board.Namespace} {
			if sb := work.triageSandbox(ns, spec.Number); sb != nil && factorycli.TriageDraft(sb) != "" {
				return sb
			}
		}
	case "Plan":
		if sb := work.issueSandbox(spec.Member, spec.Number); sb != nil && sb.GetAnnotations()[AnnotationPlanDraft] != "" {
			return sb
		}
	case "Notes":
		return notesDraftSandbox(work, spec)
	}
	return nil
}

// applyDoc is the task output to apply: the draft on sb, as it is now —
// edits included — under the document its task left.
func applyDoc(work *workState, spec boardv1alpha1.RequestSpec, sb *unstructured.Unstructured) (string, error) {
	if spec.Apply.Kind == "Notes" {
		return notesDoc(work, sb)
	}
	a := sb.GetAnnotations()
	header, draft, draftAt := a[factorycli.AnnotationTriageOutput], factorycli.TriageDraft(sb), a[AnnotationTriagedAt]
	if spec.Apply.Kind == "Plan" {
		header, draft, draftAt = a[factorycli.AnnotationPlanOutput], a[AnnotationPlanDraft], a[AnnotationPlannedAt]
	}
	// A draft from before task outputs were kept: what factory dedups its
	// comment on is the task, so name one that is stable for this draft.
	task := "board-" + sb.GetName()
	if t, err := time.Parse(time.RFC3339, draftAt); err == nil {
		task = fmt.Sprintf("%s-%d", task, t.Unix())
	}
	url := fmt.Sprintf("https://github.com/%s/%s/issues/%d", work.owner, work.repo, spec.Number)
	return factorycli.ComposeTaskOutput(spec.Apply.Kind, header, draft, url, task)
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
		if applyStamp(spec) == "" {
			continue
		}
		key := applyKey(work, spec)
		if r.Factory.IsRunning(key) {
			continue
		}
		if res, ok := r.Factory.LastResult(key); ok && res.FinishedAt.After(req.CreationTimestamp.Time) {
			continue
		}
		sb := applyDraftSandbox(work, spec)
		if sb == nil {
			continue
		}
		doc, err := applyDoc(work, spec, sb)
		if err != nil {
			logger.Error(err, "unable to compose the task output", "issue", spec.Number, "kind", spec.Apply.Kind)
			continue
		}
		token, err := r.executorToken(ctx, spec.Member)
		if err != nil {
			logger.Error(err, "apply executor has no token", "executor", spec.Member, "issue", spec.Number)
			continue
		}
		if r.Factory.StartApply(key, factorycli.ApplyOptions{Doc: doc, Action: spec.Apply.Action, GithubToken: token}) {
			logger.Info("launched factory apply", "issue", spec.Number, "kind", spec.Apply.Kind, "action", spec.Apply.Action, "member", spec.Member)
		}
	}
}

// settleApply is served by the write: the runner's result since the
// click. Success stamps the draft's sandbox, which is what the board reads
// the write as done from; the Request is only the receipt.
func (r *Reconciler) settleApply(ctx context.Context, work *workState, req *boardv1alpha1.Request, now time.Time) requestOutcome {
	spec := req.Spec
	stamp := applyStamp(spec)
	if stamp == "" {
		return requestOutcome{
			phase:   boardv1alpha1.RequestFailed,
			reason:  "Malformed",
			message: "an apply is a Triage's label or comment, a Plan's comment, or Notes' push-notes",
		}
	}
	sb := applyDraftSandbox(work, spec)
	key := applyKey(work, spec)
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
		if err := r.stampApplied(ctx, sb, spec, stamp); err != nil {
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
			message: fmt.Sprintf("there is no %s draft on %s to post", strings.ToLower(spec.Apply.Kind), applyTarget(spec)),
		}
	}
	return pendingOutcome(req, now)
}

// stampApplied marks the write done on the draft's sandbox. A posted
// triage is finished with, so its sandbox is parked too, unless a plan or
// fix is running in it.
func (r *Reconciler) stampApplied(ctx context.Context, sb *unstructured.Unstructured, spec boardv1alpha1.RequestSpec, stamp string) error {
	annotations := sb.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[stamp] = time.Now().UTC().Format(time.RFC3339)
	sb.SetAnnotations(annotations)
	if stamp == AnnotationTriagePublished && annotations[factorycli.AnnotationTaskState] != factorycli.TaskStateRunning {
		if err := unstructured.SetNestedField(sb.Object, int64(0), "spec", "replicas"); err != nil {
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

// applyTarget is what an apply writes from, for a message: its issue, or
// its sandbox.
func applyTarget(spec boardv1alpha1.RequestSpec) string {
	if spec.Sandbox != "" {
		return spec.Sandbox
	}
	return fmt.Sprintf("#%d", spec.Number)
}
