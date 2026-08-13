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

package common

import (
	"sort"
	"strconv"
	"strings"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/usagereport"
)

// ReferencedIssueList converts GetReferencedIssues output into the sorted
// issue list attached to usage records.
func ReferencedIssueList(pr *githubv39.PullRequest) []int {
	if pr == nil {
		return nil
	}
	var out []int
	for n := range GetReferencedIssues(pr) {
		out = append(out, n)
	}

	sort.Ints(out)
	return out
}

// SandboxUsageMeta derives usage-record context from a sandbox's labels
// (used by the cleanup sweeps, where per-task context is no longer around).
func SandboxUsageMeta(item *unstructured.Unstructured, repo string) usagereport.Meta {
	meta := usagereport.Meta{Repo: repo}
	labels := item.GetLabels()
	if prStr := labels["factory.gemini.google.com/pr"]; prStr != "" {
		if n, err := strconv.Atoi(prStr); err == nil {
			meta.PR = n
		}
	}
	if wf := labels["factory.gemini.google.com/workflow"]; wf != "" {
		meta.WorkflowName = wf
	}
	if numStr, ok := strings.CutPrefix(item.GetName(), "wf-issue-"); ok {
		if n, err := strconv.Atoi(numStr); err == nil {
			meta.Issue = n
			meta.Workflow = "issue-" + numStr
		}
	}
	return meta
}
