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
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/log"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
)

// A PR's auto is a watch Request. While it stands, a factory pr watch
// follows the PR up in the member's namespace, as them — care on new
// review comments and failed checks — and is relaunched as each one
// times out. Turning auto off deletes the Request, and its watch stops;
// the PR merging or closing settles it. A fix's PR gets one when the fix
// opens it, if the board's autoIterate policy is on; any other PR of the
// member's when they turn auto on.

// AnnotationWatchFiled is the PR a fix sandbox's watch Request was filed
// for, once: turning auto off on that PR deletes the Request, and it is
// not filed again.
const AnnotationWatchFiled = "board.gemini.google.com/watch-filed"

// watchKeyPrefix is the runner keys of a member's watches on a board.
func watchKeyPrefix(member, board string) string {
	return member + "/prwatch/" + board + "/"
}

// watchKey is the runner key of a member's watch of pr on a board.
func watchKey(member, board string, pr int) string {
	return watchKeyPrefix(member, board) + strconv.Itoa(pr)
}

// watchMalformed is why a watch Request cannot be served, "" when it can.
func watchMalformed(spec boardv1alpha1.RequestSpec) string {
	if spec.Item != "pr" || spec.Number <= 0 || spec.Member == "" {
		return "a watch names a pull request and a member"
	}
	return ""
}

// prEnded is how a watch's output says its PR ended: "merged", "closed",
// or "" while it is open.
func prEnded(out string) string {
	switch {
	case strings.Contains(out, " is merged. Stopping watch."):
		return "merged"
	case strings.Contains(out, " is closed. Stopping watch."):
		return "closed"
	}
	return ""
}

// ensureWatches keeps a watch running for each watch Request, and stops
// the watches whose Request is gone: auto turned off stops now, not when
// the watch times out.
func (r *Reconciler) ensureWatches(ctx context.Context, work *workState, reqs []*boardv1alpha1.Request) {
	logger := log.FromContext(ctx)
	board := work.board.Name
	wanted := map[string]bool{}
	members := map[string]bool{work.board.Namespace: true}
	for _, sb := range work.sandboxes {
		members[sb.GetNamespace()] = true
	}
	for _, req := range reqs {
		spec := req.Spec
		if watchMalformed(spec) != "" {
			continue
		}
		members[spec.Member] = true
		key := watchKey(spec.Member, board, spec.Number)
		wanted[key] = true
		if r.Factory.IsRunning(key) {
			continue
		}
		if res, ok := r.Factory.LastResult(key); ok && res.FinishedAt.After(req.CreationTimestamp.Time) &&
			(prEnded(res.Output) != "" || time.Since(res.FinishedAt) < prWatchRelaunchInterval) {
			continue
		}
		token, err := r.executorToken(ctx, spec.Member)
		if err != nil {
			continue
		}
		if r.Factory.StartPRWatch(key, factorycli.PRWatchOptions{
			Namespace:   spec.Member,
			PRURL:       fmt.Sprintf("https://github.com/%s/%s/pull/%d", work.owner, work.repo, spec.Number),
			GithubToken: token,
			Engine:      boardEngine(work.board),
			Disclose:    work.board.Spec.Policy.Disclose,
		}) {
			logger.Info("launched factory pr watch", "pr", spec.Number, "namespace", spec.Member, "board", board)
		}
	}
	for member := range members {
		for _, key := range r.Factory.Running(watchKeyPrefix(member, board)) {
			if !wanted[key] {
				r.Factory.Stop(key)
				logger.Info("stopped factory pr watch: auto is off", "key", key, "board", board)
			}
		}
	}
}

// settleWatch keeps a watch Request Running, with why its last watch
// failed if it did, until its PR ends.
func (r *Reconciler) settleWatch(work *workState, req *boardv1alpha1.Request) requestOutcome {
	spec := req.Spec
	if why := watchMalformed(spec); why != "" {
		return requestOutcome{phase: boardv1alpha1.RequestFailed, reason: "Malformed", message: why}
	}
	key := watchKey(spec.Member, work.board.Name, spec.Number)
	out := requestOutcome{phase: boardv1alpha1.RequestRunning}
	res, ok := r.Factory.LastResult(key)
	if !ok || !res.FinishedAt.After(req.CreationTimestamp.Time) {
		return out
	}
	switch prEnded(res.Output) {
	case "merged":
		return requestOutcome{phase: boardv1alpha1.RequestSucceeded, reason: "PRMerged", message: "the pull request is merged"}
	case "closed":
		return requestOutcome{phase: boardv1alpha1.RequestSucceeded, reason: "PRClosed", message: "the pull request is closed"}
	}
	if res.Err != nil && !r.Factory.IsRunning(key) {
		out.reason = "WatchFailed"
		out.message = clipMessage(strings.TrimSpace(lastLines(res.Output, 3)+"\n"+res.Err.Error()), 400)
	}
	return out
}

// fileFixWatches files a watch Request for the PR a fix opened, once, if
// the board's autoIterate policy is on.
func (r *Reconciler) fileFixWatches(ctx context.Context, work *workState, policy bool) {
	logger := log.FromContext(ctx)
	for _, sb := range work.sandboxes {
		if _, ok := factorycli.IssueOf(sb, work.repo); !ok {
			continue
		}
		prNum := sb.GetLabels()[factorycli.LabelPR]
		pr, err := strconv.Atoi(prNum)
		if err != nil || pr <= 0 || !strings.Contains(sb.GetAnnotations()["htmlURL"], "/pull/") {
			continue
		}
		annotations := sb.GetAnnotations()
		if annotations[AnnotationWatchFiled] == prNum {
			continue
		}
		if policy && !work.watching(sb.GetNamespace(), pr) {
			if err := r.fileWatch(ctx, work, sb.GetNamespace(), pr); err != nil {
				logger.Error(err, "unable to file the watch of a fix's PR", "pr", pr, "sandbox", sb.GetName())
				continue
			}
			logger.Info("filed the watch of a fix's PR", "pr", pr, "namespace", sb.GetNamespace())
		}
		annotations[AnnotationWatchFiled] = prNum
		sb.SetAnnotations(annotations)
		if err := r.Update(ctx, sb); err != nil {
			logger.Error(err, "unable to stamp the filed watch", "sandbox", sb.GetName())
		}
	}
}

// watching reports whether member's watch of pr stands.
func (w *workState) watching(member string, pr int) bool {
	for _, req := range w.activeRequests() {
		if req.Spec.Verb == boardv1alpha1.VerbWatch && req.Spec.Member == member && req.Spec.Number == pr {
			return true
		}
	}
	return false
}

// fileWatch files member's watch Request of pr, as the board's API files
// a click: owned by the board, which it wakes.
func (r *Reconciler) fileWatch(ctx context.Context, work *workState, member string, pr int) error {
	spec := boardv1alpha1.RequestSpec{
		Board:  work.board.Name,
		Verb:   boardv1alpha1.VerbWatch,
		Item:   "pr",
		Number: pr,
		Member: member,
	}
	req := &boardv1alpha1.Request{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: fmt.Sprintf("watch-pr-%d-", pr),
			Namespace:    work.board.Namespace,
			Labels: map[string]string{
				boardv1alpha1.LabelBoard: work.board.Name,
				boardv1alpha1.LabelVerb:  boardv1alpha1.VerbWatch,
			},
			Annotations: map[string]string{boardv1alpha1.AnnotationSubject: spec.Subject()},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: boardv1alpha1.GroupVersion.String(),
				Kind:       "RepoBoard",
				Name:       work.board.Name,
				UID:        work.board.UID,
				Controller: ptr.To(true),
			}},
		},
		Spec: spec,
	}
	return r.Create(ctx, req)
}
