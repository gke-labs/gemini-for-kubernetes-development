package taskoutput

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestSessionLink(t *testing.T) {
	const (
		link = "https://board.example/#/overseers/kcc/task-session/fix-r-7/recipe-fix-1"
		hint = "<sub>🤖 [Agent session](" + link + ")</sub>"
	)
	ctx := WithSessionLink(context.Background(), link)

	t.Run("the PR description ends with the hint, before the marker", func(t *testing.T) {
		doc := changeDoc(t)
		f := &fakePRs{}
		var out bytes.Buffer
		if err := ApplyAction(ctx, f.client(t), doc, "open-pr", false, &out); err != nil {
			t.Fatal(err)
		}
		if b := f.created[0].GetBody(); !strings.HasSuffix(b, "Fixes #7\n\n"+hint+marker(doc)) {
			t.Errorf("body = %q", b)
		}
	})

	t.Run("an issue comment (triage, plan, summary) ends with it, before the marker", func(t *testing.T) {
		doc := triageDoc(t, "recipe-triage-1")
		f := &fakeGitHub{}
		var out bytes.Buffer
		if err := ApplyAction(ctx, f.client(t), doc, "comment", false, &out); err != nil {
			t.Fatal(err)
		}
		if len(f.comments) != 1 || !strings.HasSuffix(f.comments[0], "\n\n"+hint+marker(doc)) {
			t.Errorf("comments = %q", f.comments)
		}
	})

	t.Run("replies and the report end with it, once", func(t *testing.T) {
		const answers = "change:\n  replies:\n    - inReplyTo: 100\n      body: Moved it.\n    - inReplyTo: 200\n      body: Done, it does now.\n  report: All addressed.\n"
		doc := prChange(t, answers)
		f := &fakeThreads{}
		var out bytes.Buffer
		if err := ApplyAction(ctx, f.client(t), doc, "post-replies", false, &out); err != nil {
			t.Fatal(err)
		}
		if b := f.review[0]["body"].(string); !strings.HasPrefix(b, "Moved it.\n\n"+hint+"\n\n<!-- factory:") {
			t.Errorf("thread reply = %q", b)
		}
		if b := f.issue[0]["body"].(string); !strings.Contains(b, "Done, it does now.\n\n"+hint+"\n\n<!-- factory:") {
			t.Errorf("quoted reply = %q", b)
		}
		if b := f.issue[1]["body"].(string); b != "All addressed.\n\n"+hint+marker(doc) {
			t.Errorf("report = %q", b)
		}
		if err := ApplyAction(ctx, f.client(t), doc, "post-replies", false, &out); err != nil {
			t.Fatal(err)
		}
		if len(f.review) != 1 || len(f.issue) != 2 {
			t.Errorf("posted again: %d review and %d issue comments", len(f.review), len(f.issue))
		}
	})

	// A care run rewrites a description that has the hint of the run that
	// opened the PR: the PR keeps one hint, the latest conversation's.
	t.Run("a rewrite keeps one hint, this apply's or the PR's", func(t *testing.T) {
		const (
			oldHint   = "<sub>🤖 [Agent session](https://board.example/#/overseers/kcc/task-session/fix-r-7/recipe-fix-0)</sub>"
			startBody = "Fixes #13935\n\n" + oldHint + "\n\n<!-- factory:task-output kind=Change task=recipe-fix-0 -->"
			// The agent copied the description, hint and all, and added
			// the release note.
			answers = "change:\n  body: |\n    Fixes #13935\n\n    ```release-note\n    NONE\n    ```\n\n    " + oldHint + "\n  report: Added the release-note block.\n"
		)
		for name, tc := range map[string]struct {
			ctx  context.Context
			want string
		}{
			"with a link":    {ctx, hint},
			"without a link": {context.Background(), oldHint},
		} {
			doc, err := Wrap("Change", answers, Target{URL: "https://github.com/o/r/pull/12"}, Source{Task: "care-1"})
			if err != nil {
				t.Fatal(err)
			}
			if err := doc.KeepTitle("fix: align", startBody); err != nil {
				t.Fatal(err)
			}
			f := &fakePRText{author: "bot", me: "bot", title: "fix: align", body: startBody}
			var out bytes.Buffer
			if err := ApplyAction(tc.ctx, f.client(t), doc, "post-replies", false, &out); err != nil {
				t.Fatal(err)
			}
			want := "Fixes #13935\n\n```release-note\nNONE\n```\n\n" + tc.want + "\n\n<!-- factory:task-output kind=Change task=recipe-fix-0 -->"
			if len(f.edits) != 1 || f.edits[0]["body"] != want {
				t.Errorf("%s: edits = %v\n%s", name, f.edits, out.String())
			}
		}
	})

	t.Run("a body that differs by its hint alone is no rewrite", func(t *testing.T) {
		doc, err := Wrap("Change", "change:\n  body: 'Fixes #1'\n  report: x\n", Target{URL: "https://github.com/o/r/pull/12"}, Source{Task: "care-1"})
		if err != nil {
			t.Fatal(err)
		}
		if err := doc.KeepTitle("t", "Fixes #1\n\n"+hint); err != nil {
			t.Fatal(err)
		}
		if c, _ := doc.ChangeSpec(); len(c.Rewrites) != 0 {
			t.Errorf("rewrites = %v", c.Rewrites)
		}
	})
}
