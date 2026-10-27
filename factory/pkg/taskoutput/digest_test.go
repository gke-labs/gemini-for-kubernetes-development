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

package taskoutput

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	githubv39 "github.com/google/go-github/v39/github"
)

func TestParseDigest(t *testing.T) {
	// 1. YAML with digest block
	yamlBlock := `
digest:
  title: "Weekly retro: 2026-10-27"
  body: "Scoreboard and insights."
  labels: ["area/retro"]
`
	d1, err := parseDigest(yamlBlock)
	if err != nil {
		t.Fatalf("parseDigest(yamlBlock): %v", err)
	}
	dig1 := d1.(*Digest)
	if dig1.Title != "Weekly retro: 2026-10-27" || dig1.Body != "Scoreboard and insights." || len(dig1.Labels) != 1 {
		t.Errorf("unexpected parsed digest: %+v", dig1)
	}

	// 2. YAML top-level
	yamlTop := `
title: "Top-level Retro"
body: "Some body content."
`
	d2, err := parseDigest(yamlTop)
	if err != nil {
		t.Fatalf("parseDigest(yamlTop): %v", err)
	}
	dig2 := d2.(*Digest)
	if dig2.Title != "Top-level Retro" || dig2.Body != "Some body content." {
		t.Errorf("unexpected parsed digest: %+v", dig2)
	}

	// 3. Markdown with heading
	md := `
# Markdown Retro

This is the body of the weekly digest.
`
	d3, err := parseDigest(md)
	if err != nil {
		t.Fatalf("parseDigest(md): %v", err)
	}
	dig3 := d3.(*Digest)
	if dig3.Title != "Markdown Retro" || !strings.Contains(dig3.Body, "This is the body") {
		t.Errorf("unexpected parsed digest: %+v", dig3)
	}
}

func TestDigestWrapMarshalParse(t *testing.T) {
	doc, err := Wrap("Digest", "title: Retro\nbody: findings\n", Target{URL: "https://github.com/owner/repo"}, Source{Task: "task-1"})
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	data, err := Marshal(doc)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	docs, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(docs) != 1 || docs[0].Kind != "Digest" {
		t.Fatalf("Parse result: %+v", docs)
	}
	spec, err := docs[0].DigestSpec()
	if err != nil {
		t.Fatalf("DigestSpec: %v", err)
	}
	if spec.Title != "Retro" || spec.Body != "findings" {
		t.Errorf("spec = %+v", spec)
	}
}

type fakeIssueServer struct {
	createdIssues []*githubv39.IssueRequest
	existing      []*githubv39.Issue
}

func (f *fakeIssueServer) client(t *testing.T) *githubv39.Client {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/issues", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var req githubv39.IssueRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			f.createdIssues = append(f.createdIssues, &req)
			num := len(f.createdIssues) + 10
			htmlURL := fmt.Sprintf("https://github.com/o/r/issues/%d", num)
			iss := &githubv39.Issue{
				Number:  &num,
				HTMLURL: &htmlURL,
				Title:   req.Title,
				Body:    req.Body,
			}
			f.existing = append(f.existing, iss)
			_ = json.NewEncoder(w).Encode(iss)
			return
		}
		// List
		_ = json.NewEncoder(w).Encode(f.existing)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(srv.URL + "/")
	return gh
}

func TestApplyOpenIssue(t *testing.T) {
	srv := &fakeIssueServer{}
	gh := srv.client(t)

	doc, err := Wrap("Digest", "title: Weekly Retro\nbody: |\n  ## Scoreboard\n", Target{URL: "https://github.com/o/r"}, Source{Task: "retro-task-1"})
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer

	// 1. Dry run
	if err := ApplyAction(context.Background(), gh, doc, "open-issue", true, &out); err != nil {
		t.Fatalf("dry-run ApplyAction: %v", err)
	}
	if len(srv.createdIssues) != 0 {
		t.Errorf("dry-run created issue")
	}
	if !strings.Contains(out.String(), "Would open an issue on o/r labelled overseer/retro") {
		t.Errorf("dry-run output = %q", out.String())
	}

	// 2. Real apply with custom trigger label
	out.Reset()
	ctx := WithTriggerLabel(context.Background(), "factory")
	if err := ApplyAction(ctx, gh, doc, "open-issue", false, &out); err != nil {
		t.Fatalf("real ApplyAction: %v", err)
	}
	if len(srv.createdIssues) != 1 {
		t.Fatalf("expected 1 created issue, got %d", len(srv.createdIssues))
	}
	created := srv.createdIssues[0]
	if created.GetTitle() != "Weekly Retro" {
		t.Errorf("title = %q, want Weekly Retro", created.GetTitle())
	}
	if !strings.Contains(created.GetBody(), "## Scoreboard") {
		t.Errorf("body missing scoreboard: %q", created.GetBody())
	}
	if !strings.Contains(created.GetBody(), "task=retro-task-1") {
		t.Errorf("body missing marker: %q", created.GetBody())
	}
	if len(created.GetLabels()) != 1 || created.GetLabels()[0] != "factory/retro" {
		t.Errorf("labels = %v, want [factory/retro]", created.GetLabels())
	}
	if doc.Target.URL != "https://github.com/o/r/issues/11" {
		t.Errorf("doc.Target.URL = %q, want https://github.com/o/r/issues/11", doc.Target.URL)
	}

	// 3. Duplicate check - apply again with same task
	out.Reset()
	doc2, err := Wrap("Digest", "title: Weekly Retro\nbody: |\n  ## Scoreboard\n", Target{URL: "https://github.com/o/r"}, Source{Task: "retro-task-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyAction(ctx, gh, doc2, "open-issue", false, &out); err != nil {
		t.Fatalf("re-apply ApplyAction: %v", err)
	}
	if len(srv.createdIssues) != 1 {
		t.Errorf("re-apply created duplicate issue; total = %d", len(srv.createdIssues))
	}
	if !strings.Contains(out.String(), "already on https://github.com/o/r/issues/11; not opening again") {
		t.Errorf("re-apply output = %q", out.String())
	}
}
