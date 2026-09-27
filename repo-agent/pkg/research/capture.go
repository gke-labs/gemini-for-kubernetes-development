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
// in factory's pkg/tasks: the conversation writes under this path and
// `factory research save-notes` pushes exactly that directory.
//
// The two are separate constants in separate modules on purpose —
// repo-agent never Go-imports factory — so nothing but a test keeps them
// equal.
const NotesRoot = "docs-exploration/research"

// NotesDir is the directory holding one session's notes.
func NotesDir(sessionID string) string { return NotesRoot + "/" + sessionID }

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
)

// NoteLimit is how long a note's file name may be, extension aside.
const NoteLimit = 64

// DefaultNote is where a capture goes when the member names no file. A
// session that never asks for anything else ends up with one readable
// document, which is the common case.
const DefaultNote = "notes.md"

// Capture is a member's request to turn part of a conversation into a
// note on the fork: what to write down, and which file to write it to.
//
// Both halves are needed because neither is derivable. A session is
// explored over many turns and only the member knows which stretch of it
// was worth keeping; and whether this belongs in the document they
// already have or wants its own is a judgement about the shape of the
// archive, not about this conversation.
type Capture struct {
	// What the member wants written down, in their words. Reaches the
	// engine verbatim as the body of the prompt.
	What string `json:"what"`
	// Note is the file within the session's directory, already
	// normalised by NormaliseNote.
	Note string `json:"note"`
}

// capturePromptData is what capture.txt sees.
type capturePromptData struct {
	Path string
	Dir  string
	What string
}

// NormaliseNote turns what the member typed into a file name inside the
// session's directory, or reports why it cannot be one.
//
// The result reaches a shell as part of a path and reaches git as part
// of a pushed tree, so this is a whitelist rather than an escape: every
// character that is not a lowercase letter or a digit becomes a dash.
// That takes "Retry loop findings" to "retry-loop-findings.md", which is
// what someone typing a title meant, and it takes "../../etc/passwd" to
// "etc-passwd", which is not an escape attempt that survives.
func NormaliseNote(name string) (string, error) {
	n := strings.ToLower(strings.TrimSpace(name))
	n = strings.TrimSuffix(n, ".md")

	var b strings.Builder
	dash := false
	for _, r := range n {
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
	stem := strings.Trim(b.String(), "-")
	if stem == "" {
		return "", fmt.Errorf("%q does not contain anything usable as a file name", name)
	}
	if len(stem) > NoteLimit {
		stem = strings.TrimRight(stem[:NoteLimit], "-")
	}
	return stem + ".md", nil
}

// Validate reports whether the capture is one this package can render.
func (c Capture) Validate() error {
	if strings.TrimSpace(c.What) == "" {
		return fmt.Errorf("say what to capture")
	}
	if c.Note == "" {
		return fmt.Errorf("a capture needs a note to write to")
	}
	if c.Note != strings.TrimSpace(c.Note) || strings.ContainsAny(c.Note, "/\\") || strings.Contains(c.Note, "..") {
		return fmt.Errorf("%q is not a note name", c.Note)
	}
	return nil
}

// Path is where the note lands, relative to the root of the checkout.
func (c Capture) Path(sessionID string) string {
	return NotesDir(sessionID) + "/" + c.Note
}

// Prompt renders the turn that asks the conversation to write the note.
//
// This is a turn in the conversation like any other, not a side channel:
// the agent has the whole exploration in its context, which is the only
// reason a one-line "what to capture" is enough to produce a document.
// It also means the member sees it happen, and can follow it with "no,
// keep the part about the retry loop" and capture again.
func (c Capture) Prompt(sessionID string) (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	t, err := template.New("capture").Parse(capturePrompt)
	if err != nil {
		return "", fmt.Errorf("parsing the capture prompt: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, capturePromptData{
		Path: c.Path(sessionID),
		Dir:  NotesDir(sessionID),
		What: strings.TrimSpace(c.What),
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
	// Note is the file the turn was asked to write. The save pushes the
	// whole directory regardless, so this is for saying what is in
	// flight, not for choosing what to push.
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
