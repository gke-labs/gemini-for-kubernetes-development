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

// Package research holds what the API and the controller both need to
// know about a research conversation before it exists: the canned
// opening prompts, the title rules, and where its notes are saved.
//
// The controller renders a canned opening into the topic it hands
// `factory recipe research`, so the prompts live on this side.
// repo-agent does not import factory (the two meet over the CLI and
// HTTP, never as one Go program).
package research

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
	"unicode"

	_ "embed"
)

// The canned openings. A "kind" is nothing more than a prompt someone
// else typed for you: the session it produces is an ordinary
// conversation, and the first thing you can do with the answer is ask a
// follow-up.
const (
	// KindOnboard is the foundation read: overview, architecture, code map.
	KindOnboard = "onboard"
	// KindActivity digests a recent window for someone catching up.
	KindActivity = "activity"
	// KindTopic is the open question — the topic text IS the prompt.
	KindTopic = "topic"
)

// Annotations on the research sandbox. The sandbox is where a session's
// state lives once the Request that started it is settled, so it is
// also where the two halves of this feature meet: the controller writes
// these, the API reads them, and the member never sees either.
const (
	// TitleAnnotation is what the session is called. Absent means the
	// list should fall back to the first line of the transcript.
	TitleAnnotation = "sandbox.gemini.google.com/research-title"
)

//go:embed onboard.txt
var onboardPrompt string

//go:embed activity.txt
var activityPrompt string

//go:embed topic.txt
var topicPrompt string

// DefaultSince is the activity window when a caller names none, which
// since the landing pane started filling the member's box rather than
// running the prompt behind a button is every caller from the UI.
//
// A month, not a fortnight: it is the window someone back from leave
// actually means, and it is the one that still has something in it for a
// repository that had a quiet couple of weeks.
//
// The window survives being a constant because activity.txt names it
// once, in its first line, and says "that window" everywhere after. The
// member edits "last month" to "last week" in the box and the whole
// prompt follows — including the session's name, which is that line.
// That is what a dropdown of three fixed windows used to buy, except
// this version also does "since the 1.4 release".
const DefaultSince = "month"

// Kickoff is the opening turn a session starts with, or the zero value
// for a session the member will type into themselves.
type Kickoff struct {
	Kind string `json:"kind,omitempty"`
	// Topic is the question, for KindTopic. It is also the session's
	// title, truncated — which is the whole reason a topic session and a
	// named session are the same thing with different fields filled in.
	Topic string `json:"topic,omitempty"`
	// Since is the window for KindActivity, e.g. "2 weeks".
	Since string `json:"since,omitempty"`
	// Title is what the session is called. Empty means derive it: from
	// the kind for a canned session, from the first prompt for a typed
	// one.
	Title string `json:"title,omitempty"`
}

// promptData is what the templates see.
type promptData struct {
	Repo    string
	HTMLURL string
	Topic   string
	Since   string
}

// Validate reports whether the kickoff is one this package can render.
//
// An unknown kind is rejected rather than defaulted: it arrives from a
// request body, and a typo that silently ran the wrong exploration
// would cost minutes of engine time before anyone noticed.
func (k Kickoff) Validate() error {
	switch k.Kind {
	case "", KindOnboard, KindActivity:
		return nil
	case KindTopic:
		if strings.TrimSpace(k.Topic) == "" {
			return fmt.Errorf("a topic session needs a topic")
		}
		return nil
	default:
		return fmt.Errorf("unknown research kind %q (onboard|activity|topic)", k.Kind)
	}
}

// Prompt renders the opening turn, or "" when there is nothing to send
// and the member is expected to type the first message.
func (k Kickoff) Prompt(repo, htmlURL string) (string, error) {
	var text string
	switch k.Kind {
	case KindOnboard:
		text = onboardPrompt
	case KindActivity:
		text = activityPrompt
	case KindTopic:
		text = topicPrompt
	default:
		return "", nil
	}
	t, err := template.New(k.Kind).Parse(text)
	if err != nil {
		return "", fmt.Errorf("parsing the %s prompt: %w", k.Kind, err)
	}
	since := strings.TrimSpace(k.Since)
	if since == "" {
		since = DefaultSince
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, promptData{
		Repo:    repo,
		HTMLURL: htmlURL,
		Topic:   strings.TrimSpace(k.Topic),
		Since:   since,
	}); err != nil {
		return "", fmt.Errorf("rendering the %s prompt: %w", k.Kind, err)
	}
	return buf.String(), nil
}

// ResolvedTitle is what the session should be called at creation, or ""
// when only the first message can say.
func (k Kickoff) ResolvedTitle() string {
	if t := strings.TrimSpace(k.Title); t != "" {
		return Truncate(t)
	}
	switch k.Kind {
	case KindOnboard:
		return "overview"
	case KindActivity:
		since := strings.TrimSpace(k.Since)
		if since == "" {
			since = DefaultSince
		}
		// The same words the prompt opens with, so a session started
		// through the API is listed as one started from the box.
		return "Changes in the last " + since
	case KindTopic:
		return Truncate(k.Topic)
	default:
		return ""
	}
}

// TitleLimit is how long a session title may be.
//
// Long enough for a real question, short enough that a list of them
// still reads as a list. A pasted paragraph is cut, not rejected: the
// member gets a title they can edit rather than an error.
const TitleLimit = 60

// Truncate reduces text to a title: first line, collapsed whitespace,
// cut at a word boundary, no trailing punctuation.
//
// Deliberately mechanical rather than asking a model to summarize. The
// first line of what someone actually typed is nearly always the better
// title — "where does the retry loop live?" beats any paraphrase of it —
// and this costs neither a turn nor a second.
func Truncate(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	line = strings.Join(strings.Fields(line), " ")
	if line == "" {
		return ""
	}
	if len(line) > TitleLimit {
		cut := line[:TitleLimit]
		// Back up to the last space so a title never ends mid-word. If
		// there is no space in the budget — one very long token — the
		// hard cut stands.
		if i := strings.LastIndex(cut, " "); i > TitleLimit/3 {
			cut = cut[:i]
		}
		line = strings.TrimRight(cut, " ") + "…"
	}
	return strings.TrimRightFunc(line, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(".,;:", r)
	})
}
