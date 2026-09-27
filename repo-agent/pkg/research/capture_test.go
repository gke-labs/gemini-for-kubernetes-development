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
	"time"
)

// The note name is typed by a member and ends up as a path component in
// a shell script and as a file in a pushed tree. Nothing that could be
// read as anything other than a file name may survive.
func TestNormaliseNote(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "notes", want: "notes.md"},
		{in: "notes.md", want: "notes.md"},
		{in: "Retry loop findings", want: "retry-loop-findings.md"},
		{in: "  scheduler   internals  ", want: "scheduler-internals.md"},
		{in: "CRD_v2", want: "crd-v2.md"},
		// A path is flattened, not honoured and not rejected: the member
		// typed a title with slashes in it more often than an attack.
		{in: "../../etc/passwd", want: "etc-passwd.md"},
		{in: "a/b/c", want: "a-b-c.md"},
		{in: ".hidden", want: "hidden.md"},
		{in: "-dashes-", want: "dashes.md"},
		{in: "note;rm -rf /", want: "note-rm-rf.md"},
		{in: "note$(id)", want: "note-id.md"},
		{in: "note\nname", want: "note-name.md"},
		// Nothing usable left is an error, not an empty name that would
		// make the path end in a slash.
		{in: "", wantErr: true},
		{in: "   ", wantErr: true},
		{in: "...", wantErr: true},
		{in: "///", wantErr: true},
		{in: ".md", wantErr: true},
	}
	for _, tc := range cases {
		got, err := NormaliseNote(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("NormaliseNote(%q) = %q, want an error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormaliseNote(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("NormaliseNote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Whatever the member typed, the result has to be a single file name —
// the prompt interpolates it into a path and the save pushes whatever
// tree that produced.
func TestNormaliseNoteAlwaysProducesANameValidateAccepts(t *testing.T) {
	for _, in := range []string{
		"notes", "../../etc/passwd", "a/b/c", "  spaced  out  ", "CRD_v2",
		"note;rm -rf /", strings.Repeat("long", 40), "über-naïve",
	} {
		note, err := NormaliseNote(in)
		if err != nil {
			continue
		}
		c := Capture{What: "the retry loop", Note: note}
		if err := c.Validate(); err != nil {
			t.Errorf("NormaliseNote(%q) = %q, which Validate rejects: %v", in, note, err)
		}
		if !strings.HasSuffix(note, ".md") {
			t.Errorf("NormaliseNote(%q) = %q, want a .md file", in, note)
		}
		if strings.Count(c.Path("s1"), "/") != strings.Count(NotesDir("s1"), "/")+1 {
			t.Errorf("NormaliseNote(%q) = %q, which leaves the session directory", in, note)
		}
	}
}

// A note name longer than the limit is cut rather than refused, and the
// cut must not leave a trailing dash.
func TestNormaliseNoteCutsLongNames(t *testing.T) {
	got, err := NormaliseNote(strings.Repeat("ab ", 60))
	if err != nil {
		t.Fatalf("NormaliseNote: %v", err)
	}
	stem := strings.TrimSuffix(got, ".md")
	if len(stem) > NoteLimit {
		t.Errorf("stem is %d characters, want at most %d", len(stem), NoteLimit)
	}
	if strings.HasSuffix(stem, "-") {
		t.Errorf("NormaliseNote cut mid-separator: %q", got)
	}
}

func TestCaptureValidate(t *testing.T) {
	cases := []struct {
		name    string
		c       Capture
		wantErr bool
	}{
		{name: "ordinary", c: Capture{What: "the retry loop", Note: "notes.md"}},
		{name: "no what", c: Capture{Note: "notes.md"}, wantErr: true},
		{name: "blank what", c: Capture{What: "  \n ", Note: "notes.md"}, wantErr: true},
		{name: "no note", c: Capture{What: "the retry loop"}, wantErr: true},
		// Validate is the last gate before the name reaches a path, so
		// it refuses a name NormaliseNote would never have produced.
		{name: "path", c: Capture{What: "x", Note: "a/b.md"}, wantErr: true},
		{name: "walk", c: Capture{What: "x", Note: "..md"}, wantErr: true},
		{name: "backslash", c: Capture{What: "x", Note: `a\b.md`}, wantErr: true},
		{name: "untrimmed", c: Capture{What: "x", Note: " notes.md"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.c.Validate()
			if tc.wantErr != (err != nil) {
				t.Errorf("Validate() = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// The prompt's whole job is to name the one file the conversation may
// write and to carry the member's words through unchanged.
func TestCapturePromptNamesTheFileAndCarriesTheRequest(t *testing.T) {
	what := "how the scheduler picks a node, with the file:line refs"
	c := Capture{What: what, Note: "scheduling.md"}
	got, err := c.Prompt("sess-1")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	want := "docs-exploration/research/sess-1/scheduling.md"
	if !strings.Contains(got, want) {
		t.Errorf("prompt does not name %q:\n%s", want, got)
	}
	if !strings.Contains(got, what) {
		t.Errorf("prompt does not carry the request verbatim:\n%s", got)
	}
	if !strings.Contains(got, NotesDir("sess-1")) {
		t.Errorf("prompt does not fence writes to the session directory:\n%s", got)
	}
	// Both branches have to be in the one text: the member picks an
	// existing note as often as a new one, and the checkout — not the
	// branch, which may lag a save behind — is what says which.
	for _, phrase := range []string{"already exists", "does not exist"} {
		if !strings.Contains(got, phrase) {
			t.Errorf("prompt does not handle the %q case:\n%s", phrase, got)
		}
	}
	if strings.Contains(got, "<no value>") || strings.Contains(got, "{{") {
		t.Errorf("prompt left a template hole:\n%s", got)
	}
}

// An invalid capture must not render: a prompt is the one thing here
// that cannot be taken back once sent.
func TestCapturePromptRefusesAnInvalidCapture(t *testing.T) {
	if got, err := (Capture{Note: "notes.md"}).Prompt("sess-1"); err == nil {
		t.Errorf("Prompt rendered a capture with nothing to capture:\n%s", got)
	}
	if got, err := (Capture{What: "x", Note: "../evil.md"}).Prompt("sess-1"); err == nil {
		t.Errorf("Prompt rendered a note outside the session directory:\n%s", got)
	}
}

func TestPendingRoundTrips(t *testing.T) {
	want := Pending{Note: "notes.md", At: time.Now().UTC().Truncate(time.Second)}
	got, ok := DecodePending(want.Encode())
	if !ok {
		t.Fatalf("DecodePending(%q) reported not-a-pending", want.Encode())
	}
	if got.Note != want.Note || !got.At.Equal(want.At) {
		t.Errorf("round trip changed the payload:\n got %+v\nwant %+v", got, want)
	}
}

// The annotation value is the only thing bounding how long the
// controller waits on a session, so a value it cannot read has to be
// rejected outright rather than defaulted into an unexpiring wait.
func TestDecodePendingRejectsWhatItCannotRead(t *testing.T) {
	for _, name := range []string{"", "not base64!!", "bm90IGpzb24"} {
		if p, ok := DecodePending(name); ok {
			t.Errorf("DecodePending(%q) = %+v, want not-a-pending", name, p)
		}
	}
	// Well-formed JSON with no clock on it is the case that matters: it
	// decodes fine and would never time out.
	noClock := Pending{Note: "notes.md"}.Encode()
	if p, ok := DecodePending(noClock); ok {
		t.Errorf("DecodePending accepted a pending save with no timestamp: %+v", p)
	}
}

// The conversation writes here and `factory research save-notes` pushes
// exactly this directory. They are separate constants in separate
// modules — repo-agent never imports factory — so only this keeps them
// equal.
func TestNotesRootMatchesFactory(t *testing.T) {
	if NotesRoot != "docs-exploration/research" {
		t.Errorf("NotesRoot = %q; factory's save_notes.sh pushes docs-exploration/research", NotesRoot)
	}
	if got := NotesDir("sess-1"); got != "docs-exploration/research/sess-1" {
		t.Errorf("NotesDir = %q", got)
	}
}
