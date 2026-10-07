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
// the fix's newest result: when, why it failed if it did, and its Change.
// The PR it opened is on the sandbox already (open-pr aliased it).
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
	var applied []string
	if res.Err != nil {
		annotations[AnnotationFixError] = fixErrorLine(res)
	} else {
		delete(annotations, AnnotationFixError)
		applied = []string{"open-pr"}
	}
	// The Change, kept whether or not its PR opened: Open draft PR is
	// the retry.
	factorycli.KeepOutput(annotations, factorycli.AnnotationFixRun, factorycli.HarvestedTaskOutput(res.Output), res.FinishedAt, applied...)
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
