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
// opening prompts, the title rules, and how a kickoff rides on a
// mailbox claim.
//
// The prompts live here rather than in factory because factory cannot
// send them. Creating an acpd session takes the engine credential, and
// `factory research start` must never hold one — it would land on the
// sandbox's disk, where the agent running in it could read it. So the
// opening turn is sent from the cluster side, by whoever has the key,
// and the text has to be somewhere the cluster side can reach. repo-agent
// does not import factory (the two meet over the CLI and HTTP, never as
// one Go program), so "somewhere" is here.
package research

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"
	"time"
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
// state lives once the mailbox claim is gone, so it is also where the
// two halves of this feature meet: the controller writes these, the API
// reads them, and the member never sees either.
const (
	// TitleAnnotation is what the session is called. Absent means the
	// list should fall back to the first line of the transcript.
	TitleAnnotation = "sandbox.gemini.google.com/research-title"
	// KickoffAnnotation is an encoded Kickoff, present only while the
	// opening turn is still owed. Its absence IS the receipt: the
	// controller deletes it once the prompt is in, so nothing can send
	// the same opening twice.
	KickoffAnnotation = "sandbox.gemini.google.com/research-kickoff"
	// KickoffErrorAnnotation says why an owed opening turn was given up
	// on. A session whose kickoff never landed should say so rather than
	// sit empty looking like a question nobody answered.
	KickoffErrorAnnotation = "sandbox.gemini.google.com/research-kickoff-error"
)

//go:embed onboard.txt
var onboardPrompt string

//go:embed activity.txt
var activityPrompt string

//go:embed topic.txt
var topicPrompt string

// DefaultSince is the activity window when a caller names none.
const DefaultSince = "2 weeks"

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
		return "what happened · " + since
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

// Claim is a request for a session, as it rides in the board's mailbox
// annotation: `<member>|<RFC3339>|<encoded kickoff>`, the third field
// present only for a canned exploration.
//
// Three readers now — the handler that files it, the controller that
// serves it, and the list that shows it as a pending row — so the format
// lives here rather than being spelled out in each.
type Claim struct {
	Member  string
	At      time.Time
	Kickoff Kickoff
}

// Encode renders the claim as a mailbox value.
func (c Claim) Encode() string {
	value := c.Member + "|" + c.At.UTC().Format(time.RFC3339)
	if encoded := c.Kickoff.Encode(); encoded != "" {
		value += "|" + encoded
	}
	return value
}

// DecodeClaim reads a mailbox value, reporting false for anything that
// is not one.
//
// A malformed entry is rejected rather than repaired. The timestamp in
// particular is required, not defaulted: it is what the claim's TTL
// measures, and a claim with no clock on it could never expire. The
// kickoff is the exception — a third field that will not decode costs
// the session its opening prompt, not its existence.
func DecodeClaim(value string) (Claim, bool) {
	member, rest, _ := strings.Cut(value, "|")
	if member == "" {
		return Claim{}, false
	}
	at, encoded, _ := strings.Cut(rest, "|")
	when, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return Claim{}, false
	}
	return Claim{Member: member, At: when, Kickoff: DecodeKickoff(encoded)}, true
}

// Encode renders the kickoff for the third field of a mailbox claim.
//
// Base64 of JSON rather than a delimited string: a topic is free text
// from a textarea, so it can hold the "|" the claim is split on, a
// newline, or anything else a person can type. Encoding sidesteps the
// question entirely, and an annotation value has room for it.
//
// The zero kickoff encodes to "", so a plain "new conversation" claim
// is byte-identical to what this code shipped with.
func (k Kickoff) Encode() string {
	if k == (Kickoff{}) {
		return ""
	}
	buf, err := json.Marshal(k)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

// DecodeKickoff reads the third claim field. A field that is missing or
// malformed yields the zero kickoff: a hand-edited annotation should
// cost the session its opening prompt, not stop it being created.
func DecodeKickoff(field string) Kickoff {
	if field == "" {
		return Kickoff{}
	}
	buf, err := base64.RawURLEncoding.DecodeString(field)
	if err != nil {
		return Kickoff{}
	}
	var k Kickoff
	if err := json.Unmarshal(buf, &k); err != nil {
		return Kickoff{}
	}
	if err := k.Validate(); err != nil {
		return Kickoff{}
	}
	return k
}
