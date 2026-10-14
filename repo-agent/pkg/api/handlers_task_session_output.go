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

package api

import (
	"context"

	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
)

// refreshSessionOutput takes the session's run's task output, as the
// sandbox's task server serves it now, into the draft the board stores
// (factorycli.RefreshOutput): a result the agent wrote again in a later
// turn of the conversation, which factory re-wraps, or an optional one
// it wrote only then (research's notes). One read per call; a run still
// running, a Change (factory never re-wraps one: its push facts are the
// push step's) and a read that fails leave the draft as it is.
func (s *Server) refreshSessionOutput(ctx context.Context, conn *taskSessionConn) {
	a := conn.sandbox.GetAnnotations()
	run, ok := factorycli.SessionRun(a, conn.task)
	if !ok || run.Task == "" || run.Kind == "" || run.Kind == "Change" || run.State == "Running" {
		return
	}
	data, err := conn.client.ReadTaskFile(ctx, run.Task, taskOutputFile)
	if err != nil {
		return
	}
	if !factorycli.RefreshOutput(a, run.Key, string(data)) {
		return
	}
	set := map[string]string{}
	for _, key := range []string{factorycli.OutputAnnotation(run.Key), factorycli.AppliedAnnotation(run.Key), factorycli.OutputSourceAnnotation(run.Key)} {
		set[key] = a[key]
	}
	if err := s.updateResearchAnnotations(ctx, conn.namespace, conn.sandbox.GetName(), set); err != nil {
		klog.FromContext(ctx).Error(err, "storing the session's rewritten task output", "sandbox", conn.sandbox.GetName(), "task", run.Task)
		return
	}
	conn.sandbox.SetAnnotations(a)
}
