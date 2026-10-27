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

func TestDigestParsing(t *testing.T) {
	// 1. From markdown
	doc, err := Wrap("Digest", "# Weekly Retro\nScoreboard and notes.\n", Target{URL: "https://github.com/o/r"}, Source{Task: "t1"})
	if err != nil {
		t.Fatal(err)
	}
	dig, err := doc.DigestSpec()
	if err != nil {
		t.Fatal(err)
	}
	if dig.Markdown != "# Weekly Retro\nScoreboard and notes." {
		t.Errorf("got markdown %q", dig.Markdown)
	}

	// 2. From YAML with digest block
	rawYAML := "digest:\n  title: Custom Title\n  markdown: \"## Scoreboard\\nAll good.\"\n"
	docYAML, err := Wrap("Digest", rawYAML, Target{URL: "https://github.com/o/r"}, Source{Task: "t2"})
	if err != nil {
		t.Fatal(err)
	}
	digYAML, err := docYAML.DigestSpec()
	if err != nil {
		t.Fatal(err)
	}
	if digYAML.Title != "Custom Title" || digYAML.Markdown != "## Scoreboard\nAll good." {
		t.Errorf("got %+v", digYAML)
	}

	// 3. Empty markdown rejected
	if _, err := Wrap("Digest", "", Target{URL: "https://github.com/o/r"}, Source{}); err == nil {
		t.Error("empty digest accepted")
	}

	// 4. DigestSpec on wrong kind
	planDoc := &Document{Kind: "Plan"}
	if _, err := planDoc.DigestSpec(); err == nil {
		t.Error("DigestSpec succeeded on Plan doc")
	}
}

func TestDigestActions(t *testing.T) {
	acts := DefaultActions("Digest")
	if len(acts) != 3 {
		t.Fatalf("expected 3 default actions, got %d", len(acts))
	}
	if acts[0].Verb != "edit" || acts[1].Verb != "open-issue" || acts[2].Verb != "reject" {
		t.Errorf("unexpected actions: %+v", acts)
	}
	if err := ValidateActions("Digest", acts); err != nil {
		t.Errorf("ValidateActions failed: %v", err)
	}
}

type fakeIssueServer struct {
	createdIssues []*githubv39.IssueRequest
	existingList  []*githubv39.Issue
}

func (f *fakeIssueServer) client(t *testing.T) *githubv39.Client {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/issues", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(f.existingList)
		case http.MethodPost:
			var req githubv39.IssueRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			f.createdIssues = append(f.createdIssues, &req)
			num := len(f.createdIssues) + 100
			htmlURL := fmt.Sprintf("https://github.com/o/r/issues/%d", num)
			resp := githubv39.Issue{
				Number:  &num,
				HTMLURL: &htmlURL,
				Title:   req.Title,
				Body:    req.Body,
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(srv.URL + "/")
	return gh
}

func TestApplyOpenIssue(t *testing.T) {
	fake := &fakeIssueServer{}
	gh := fake.client(t)

	doc, err := Wrap("Digest", "# Retro 2026-W43\n\nWeekly scoreboard and observations.",
		Target{URL: "https://github.com/o/r"}, Source{Task: "recipe-retro-1"})
	if err != nil {
		t.Fatal(err)
	}

	ctx := WithTriggerLabel(context.Background(), "overseer")

	// 1. Dry run
	var outDry bytes.Buffer
	if err := ApplyAction(ctx, gh, doc, "open-issue", true, &outDry); err != nil {
		t.Fatal(err)
	}
	if len(fake.createdIssues) != 0 {
		t.Errorf("dry run created an issue: %v", fake.createdIssues)
	}
	if !strings.Contains(outDry.String(), "open issue") || !strings.Contains(outDry.String(), "overseer/retro") {
		t.Errorf("dry run output: %s", outDry.String())
	}

	// 2. Real run
	var outReal bytes.Buffer
	if err := ApplyAction(ctx, gh, doc, "open-issue", false, &outReal); err != nil {
		t.Fatal(err)
	}
	if len(fake.createdIssues) != 1 {
		t.Fatalf("expected 1 created issue, got %d", len(fake.createdIssues))
	}
	created := fake.createdIssues[0]
	if created.GetTitle() != "Retro 2026-W43" {
		t.Errorf("title = %q, want Retro 2026-W43", created.GetTitle())
	}
	if !strings.Contains(created.GetBody(), "Weekly scoreboard and observations.") {
		t.Errorf("body missing markdown content: %s", created.GetBody())
	}
	if !strings.Contains(created.GetBody(), "task=recipe-retro-1") {
		t.Errorf("body missing task marker: %s", created.GetBody())
	}
	if len(created.GetLabels()) != 1 || created.GetLabels()[0] != "overseer/retro" {
		t.Errorf("labels = %v, want [overseer/retro]", created.GetLabels())
	}
	if doc.Target.URL != "https://github.com/o/r/issues/101" {
		t.Errorf("target URL = %s, want https://github.com/o/r/issues/101", doc.Target.URL)
	}

	// 3. Idempotent re-run: simulate issue in list
	num := 101
	body := created.GetBody()
	htmlURL := "https://github.com/o/r/issues/101"
	fake.existingList = []*githubv39.Issue{
		{
			Number:  &num,
			Body:    &body,
			HTMLURL: &htmlURL,
		},
	}
	var outIdempotent bytes.Buffer
	if err := ApplyAction(ctx, gh, doc, "open-issue", false, &outIdempotent); err != nil {
		t.Fatal(err)
	}
	if len(fake.createdIssues) != 1 {
		t.Errorf("idempotent run created duplicate issue; total: %d", len(fake.createdIssues))
	}
	if !strings.Contains(outIdempotent.String(), "already on o/r#101; not opening again") {
		t.Errorf("output = %s, want already on o/r#101", outIdempotent.String())
	}
}
