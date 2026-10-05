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

package research

import (
	"strings"
	"testing"
)

func TestPromptRendersTheRepositoryIn(t *testing.T) {
	for _, kind := range []string{KindOnboard, KindActivity, KindTopic} {
		k := Kickoff{Kind: kind, Topic: "compare with Envoy"}
		got, err := k.Prompt("repo-agent", "https://github.com/gke-labs/repo-agent")
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if !strings.Contains(got, "repo-agent") {
			t.Errorf("%s prompt does not name the repository:\n%s", kind, got)
		}
		// The templates must not reach the engine with an unrendered
		// action in them, which is what a renamed field looks like.
		if strings.Contains(got, "{{") || strings.Contains(got, "<no value>") {
			t.Errorf("%s prompt has an unrendered field:\n%s", kind, got)
		}
	}
}

// The canned reads reach the engine through the member's own box now —
// the landing pane fills it with this text rather than starting a
// session behind it — so what runs is an ordinary typed session, and its
// name is Truncate of what was sent. That makes the first line of each
// prompt the name in the rail, which is why it is a title and not the
// first sentence of the instruction.
func TestCannedPromptsOpenWithTheirTitle(t *testing.T) {
	for _, tc := range []struct{ kind, want string }{
		{KindOnboard, "Overview of the repo"},
		{KindActivity, "Changes in the last month"},
	} {
		got, err := Kickoff{Kind: tc.kind}.Prompt("repo-agent", "https://github.com/gke-labs/repo-agent")
		if err != nil {
			t.Fatalf("%s: %v", tc.kind, err)
		}
		if title := Truncate(got); title != tc.want {
			t.Errorf("sent as a question, %s names its session %q, want %q", tc.kind, title, tc.want)
		}
	}
}

// The window is named once, in the line that is also the title.
//
// That single mention is what lets the box replace the window picker:
// change "last month" to "last week" — or to "since the 1.4 release" —
// and the prompt and the name it will be listed under change together,
// because everything after the first line says "that window". A second
// mention would sit there contradicting the edit.
func TestActivityNamesItsWindowOnce(t *testing.T) {
	got, err := Kickoff{Kind: KindActivity}.Prompt("repo-agent", "https://github.com/gke-labs/repo-agent")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(got, DefaultSince); n != 1 {
		t.Errorf("the activity prompt says %q %d times, want once:\n%s", DefaultSince, n, got)
	}
}

func TestPromptIsEmptyForATypedSession(t *testing.T) {
	got, err := Kickoff{}.Prompt("repo-agent", "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("a session with no kind should send nothing, got %q", got)
	}
}

func TestActivityDefaultsItsWindow(t *testing.T) {
	got, err := Kickoff{Kind: KindActivity}.Prompt("repo-agent", "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, DefaultSince) {
		t.Errorf("activity prompt with no window should say %q:\n%s", DefaultSince, got)
	}
}

func TestValidate(t *testing.T) {
	for _, tc := range []struct {
		name string
		k    Kickoff
		ok   bool
	}{
		{"typed session", Kickoff{}, true},
		{"onboard", Kickoff{Kind: KindOnboard}, true},
		{"topic with text", Kickoff{Kind: KindTopic, Topic: "why?"}, true},
		{"topic with none", Kickoff{Kind: KindTopic}, false},
		{"topic of spaces", Kickoff{Kind: KindTopic, Topic: "   "}, false},
		{"invented kind", Kickoff{Kind: "onboarding"}, false},
	} {
		if err := tc.k.Validate(); (err == nil) != tc.ok {
			t.Errorf("%s: Validate() = %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}

func TestResolvedTitle(t *testing.T) {
	for _, tc := range []struct {
		k    Kickoff
		want string
	}{
		{Kickoff{Kind: KindOnboard}, "overview"},
		{Kickoff{Kind: KindActivity}, "Changes in the last month"},
		{Kickoff{Kind: KindActivity, Since: "3 months"}, "Changes in the last 3 months"},
		{Kickoff{Kind: KindTopic, Topic: "compare with Envoy"}, "compare with Envoy"},
		// An explicit name beats the derivation, whatever the kind.
		{Kickoff{Kind: KindOnboard, Title: "first read"}, "first read"},
		// A typed session has no title until its first message.
		{Kickoff{}, ""},
	} {
		if got := tc.k.ResolvedTitle(); got != tc.want {
			t.Errorf("ResolvedTitle(%+v) = %q, want %q", tc.k, got, tc.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"where does the retry loop live?", "where does the retry loop live?"},
		{"  spaced   out  ", "spaced out"},
		{"first line\nsecond line", "first line"},
		{"trailing punctuation.", "trailing punctuation"},
		{"", ""},
		{"\n\n", ""},
	} {
		if got := Truncate(tc.in); got != tc.want {
			t.Errorf("Truncate(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	long := "explain how the reconciler decides that a research claim has been served and when it gives up"
	got := Truncate(long)
	if len([]rune(got)) > TitleLimit+1 { // +1 for the ellipsis
		t.Errorf("Truncate did not shorten: %q (%d runes)", got, len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("a cut title should say it was cut: %q", got)
	}
	// Cut at a word boundary, so the last word is a whole one.
	if strings.Contains(strings.TrimSuffix(got, "…"), "  ") {
		t.Errorf("collapsed whitespace expected: %q", got)
	}
}
