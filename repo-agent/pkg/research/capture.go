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
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"
	"time"

	_ "embed"
)

//go:embed capture.txt
var capturePrompt string

// NotesRoot is where a session's notes live, both in the checkout and on
// the notes branch of the member's fork. It must match ResearchNotesDir
// in factory's pkg/tasks: the conversation writes here and `factory
// research save-notes` pushes exactly this file.
//
// The two are separate constants in separate modules on purpose —
// repo-agent never Go-imports factory — so nothing but a test keeps them
// equal.
const NotesRoot = "docs-exploration/research"

// NotesPath is where one session's note lives, relative to the root of
// the checkout. A file and not a directory: a conversation writes one
// document, and a directory per session was a container for one file.
func NotesPath(note string) string { return NotesRoot + "/" + note }

// Annotations for a save that has been asked for but not yet made.
const (
	// CaptureAnnotation is an encoded Pending, present only while a save
	// is owed. As with the kickoff, its absence IS the receipt: the
	// controller deletes it once the notes are pushed.
	CaptureAnnotation = "sandbox.gemini.google.com/research-capture"
	// CaptureErrorAnnotation says why an owed save was given up on. A
	// member who asked for a note and got nothing should be told, rather
	// than going to look for it on the fork.
	CaptureErrorAnnotation = "sandbox.gemini.google.com/research-capture-error"
	// NoteAnnotation is the file this session writes to, decided at the
	// first capture and never again.
	//
	// Pinned rather than derived each time, because it is derived from
	// the title and the title can change. A session renamed between two
	// captures would otherwise push its second note under a second name
	// and leave the first one orphaned under one nothing refers to any
	// more.
	NoteAnnotation = "sandbox.gemini.google.com/research-note"
)

// NoteLimit is how long a note's file name may be, extension aside.
const NoteLimit = 64

// DefaultWhat is what gets captured when the member says nothing. It is
// the common case and the reason saving is one click: the answer to
// "which part of this was worth keeping" is almost always "the part
// that was not obvious", and the conversation knows which part that was
// better than a form does.
const DefaultWhat = "Everything in this conversation worth keeping: what was asked, " +
	"what was found, and what is still open."

// Capture is a request to turn a conversation into a note on the fork.
type Capture struct {
	// Note is the file under NotesRoot, already normalised: see
	// NoteName and UniqueNote.
	Note string `json:"note"`
	// What the member wants written down, in their words. Reaches the
	// engine verbatim as the body of the prompt. Empty is the ordinary
	// case and means DefaultWhat.
	What string `json:"what,omitempty"`
}

// capturePromptData is what capture.txt sees.
type capturePromptData struct {
	Path string
	What string
}

// slug folds a title or a file name down to the characters that are safe
// everywhere this ends up.
//
// The result reaches a shell as part of a path and reaches git as part
// of a pushed tree, so this is a whitelist rather than an escape: every
// character that is not a lowercase letter or a digit becomes a dash.
// That takes "Retry loop findings" to "retry-loop-findings", which is
// what someone typing a title meant, and it takes "../../etc/passwd" to
// "etc-passwd", which is not an escape attempt that survives.
//
// Returns "" for anything with nothing usable in it.
func slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			dash = false
		default:
			// Collapse any run of separators, and never open with one:
			// a leading dot would hide the file, a leading dash would
			// read as a flag.
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > NoteLimit {
		out = strings.TrimRight(out[:NoteLimit], "-")
	}
	return out
}

// NoteName is the file a session's write-up wants to be: its name,
// folded to a path segment, with .md on it.
//
// The name and not the id, because this file is the note's address for
// as long as the fork exists, and it is read by people. A session id is
// a handle for the machinery — it names the sandbox, the label and the
// single-flight key — and none of that survives on a branch of prose.
// It remains the fallback: a session that has not been named yet still
// has one, and an id is a worse file name than a title but a much
// better one than none.
func NoteName(title, sessionID string) string {
	if s := slug(title); s != "" {
		return s + ".md"
	}
	return sessionID + ".md"
}

// UniqueNote is name, or the first name-N.md that nothing has taken.
//
// Two sessions can genuinely want the same file — the canned
// explorations are the common case, since every "first read" of a repo
// is called the same thing — and a save overwrites what is there, so
// sharing a name is not two sessions collaborating on a note. It is the
// second one replacing the first one's.
func UniqueNote(name string, taken map[string]bool) string {
	if !taken[name] {
		return name
	}
	stem := strings.TrimSuffix(name, ".md")
	// Terminates: taken is finite, so some suffix is free.
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d.md", stem, i)
		if !taken[candidate] {
			return candidate
		}
	}
}

// Validate reports whether the capture is one this package can render.
//
// The name is derived here rather than taken from the member, so this
// is the assertion that it stayed one path component: it reaches a
// shell as part of a path and git as part of a pushed tree.
func (c Capture) Validate() error {
	if c.Note == "" {
		return fmt.Errorf("a capture needs a note to write to")
	}
	if c.Note != strings.TrimSpace(c.Note) || strings.ContainsAny(c.Note, "/\\") || strings.Contains(c.Note, "..") {
		return fmt.Errorf("%q is not a note name", c.Note)
	}
	return nil
}

// Path is where the note lands, relative to the root of the checkout.
func (c Capture) Path() string { return NotesPath(c.Note) }

// Prompt renders the turn that asks the conversation to write the note.
//
// This is a turn in the conversation like any other, not a side channel:
// the agent has the whole exploration in its context, which is the only
// reason a canned "write this up" is enough to produce a document. It
// also means the member sees it happen, and can follow it with "no, keep
// the part about the retry loop" and capture again.
func (c Capture) Prompt() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	what := strings.TrimSpace(c.What)
	if what == "" {
		what = DefaultWhat
	}
	t, err := template.New("capture").Parse(capturePrompt)
	if err != nil {
		return "", fmt.Errorf("parsing the capture prompt: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, capturePromptData{
		Path: c.Path(),
		What: what,
	}); err != nil {
		return "", fmt.Errorf("rendering the capture prompt: %w", err)
	}
	return buf.String(), nil
}

// Pending is a save the conversation has been asked for and the
// controller has not yet made: which note was asked for, and when.
//
// It rides on the sandbox rather than in the API's memory because the
// API is replicated and stateless, and because the wait is open-ended —
// the turn that writes the note may run for minutes, and the save has to
// survive whichever replica served the request going away.
type Pending struct {
	// Note is the file the turn was asked to write, and the one the
	// save pushes. It travels with the request rather than being
	// re-derived by the controller: what gets pushed has to be what the
	// prompt was told to write, whatever the session has been renamed
	// to since.
	Note string `json:"note"`
	// At is when the prompt was sent. It bounds the wait, and it is what
	// tells a finished save-notes run apart from one left over from an
	// earlier capture on the same session.
	At time.Time `json:"at"`
}

// Encode renders the pending save as an annotation value.
func (p Pending) Encode() string {
	buf, err := json.Marshal(p)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

// DecodePending reads the annotation, reporting false for anything that
// is not one.
//
// Unlike a kickoff, a malformed value is rejected rather than defaulted:
// the timestamp is what bounds the wait, and a pending save with no clock
// on it would have the controller poll a session forever.
func DecodePending(value string) (Pending, bool) {
	if value == "" {
		return Pending{}, false
	}
	buf, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return Pending{}, false
	}
	var p Pending
	if err := json.Unmarshal(buf, &p); err != nil {
		return Pending{}, false
	}
	if p.At.IsZero() {
		return Pending{}, false
	}
	return p, true
}
