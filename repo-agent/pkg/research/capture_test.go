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

// The note's name comes from the session's title, and it ends up as a
// path component in a shell script and as a file in a pushed tree.
// Nothing that could be read as anything other than a file name may
// survive.
func TestNoteName(t *testing.T) {
	cases := []struct {
		title, sessionID, want string
	}{
		{"Where the retry loop terminates", "s1", "where-the-retry-loop-terminates.md"},
		{"first read", "s1", "first-read.md"},
		{"  scheduler   internals  ", "s1", "scheduler-internals.md"},
		{"CRD_v2 / storage", "s1", "crd-v2-storage.md"},
		// A path is flattened, not honoured and not rejected: a member
		// types a title with slashes in it more often than an attack.
		{"../../etc/passwd", "s1", "etc-passwd.md"},
		{".hidden", "s1", "hidden.md"},
		{"-dashes-", "s1", "dashes.md"},
		{"note;rm -rf /", "s1", "note-rm-rf.md"},
		{"note$(id)", "s1", "note-id.md"},
		{"note\nname", "s1", "note-name.md"},
		// A title with nothing usable in it is the same as none: the id
		// is a worse file name than a name and a much better one than an
		// empty path component.
		{"", "s1", "s1.md"},
		{"   ", "s1", "s1.md"},
		{"…", "s1", "s1.md"},
		{"...", "s1", "s1.md"},
		{"///", "s1", "s1.md"},
	}
	for _, tc := range cases {
		if got := NoteName(tc.title, tc.sessionID); got != tc.want {
			t.Errorf("NoteName(%q, %q) = %q, want %q", tc.title, tc.sessionID, got, tc.want)
		}
	}
}

// Whatever the session is called, the result has to be a single file
// name — the prompt interpolates it into a path and the save pushes
// whatever that produced.
func TestNoteNameAlwaysProducesANameValidateAccepts(t *testing.T) {
	for _, title := range []string{
		"notes", "../../etc/passwd", "a/b/c", "  spaced  out  ", "CRD_v2",
		"note;rm -rf /", strings.Repeat("long", 40), "über-naïve", "", "…",
	} {
		note := NoteName(title, "s1")
		c := Capture{Note: note}
		if err := c.Validate(); err != nil {
			t.Errorf("NoteName(%q) = %q, which Validate rejects: %v", title, note, err)
		}
		if !strings.HasSuffix(note, ".md") {
			t.Errorf("NoteName(%q) = %q, want a .md file", title, note)
		}
		if got, want := c.Path(), NotesRoot+"/"+note; got != want {
			t.Errorf("NoteName(%q) = %q, which lands at %q, not %q", title, note, got, want)
		}
	}
}

// A title longer than the limit is cut rather than refused, and the cut
// must not leave a trailing dash.
func TestNoteNameCutsLongTitles(t *testing.T) {
	got := NoteName(strings.Repeat("ab ", 60), "s1")
	stem := strings.TrimSuffix(got, ".md")
	if len(stem) > NoteLimit {
		t.Errorf("stem is %d characters, want at most %d", len(stem), NoteLimit)
	}
	if strings.HasSuffix(stem, "-") {
		t.Errorf("NoteName cut mid-separator: %q", got)
	}
}

func TestCaptureValidate(t *testing.T) {
	cases := []struct {
		name    string
		c       Capture
		wantErr bool
	}{
		{name: "ordinary", c: Capture{Note: "retry-loop.md", What: "the retry loop"}},
		// What is the member's one optional input: saving is a button
		// with nothing to fill in, and an empty request means the whole
		// conversation.
		{name: "no what", c: Capture{Note: "retry-loop.md"}},
		{name: "no note", c: Capture{}, wantErr: true},
		// Validate is the last gate before the name reaches a path, so it
		// refuses what NoteName would never have produced.
		{name: "path note", c: Capture{Note: "a/b.md"}, wantErr: true},
		{name: "walk note", c: Capture{Note: "..md"}, wantErr: true},
		{name: "backslash note", c: Capture{Note: `a\b.md`}, wantErr: true},
		{name: "untrimmed note", c: Capture{Note: " notes.md"}, wantErr: true},
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

// Two sessions can genuinely want one name — every canned "first read"
// of a repository is called the same thing — and a save overwrites what
// is there, so sharing one is not collaboration.
func TestUniqueNote(t *testing.T) {
	taken := map[string]bool{"first-read.md": true, "first-read-2.md": true}
	if got := UniqueNote("first-read.md", taken); got != "first-read-3.md" {
		t.Errorf("UniqueNote = %q, want first-read-3.md", got)
	}
	if got := UniqueNote("retry-loop.md", taken); got != "retry-loop.md" {
		t.Errorf("UniqueNote took a free name to %q", got)
	}
	if got := UniqueNote("first-read.md", nil); got != "first-read.md" {
		t.Errorf("UniqueNote with nothing taken = %q", got)
	}
}

// The prompt's whole job is to name the one file the conversation may
// write and to carry the member's words through unchanged.
func TestCapturePromptNamesTheFileAndCarriesTheRequest(t *testing.T) {
	what := "how the scheduler picks a node, with the file:line refs"
	c := Capture{Note: "how-scheduling-works.md", What: what}
	got, err := c.Prompt()
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	want := "docs-exploration/research/how-scheduling-works.md"
	if !strings.Contains(got, want) {
		t.Errorf("prompt does not name %q:\n%s", want, got)
	}
	if !strings.Contains(got, what) {
		t.Errorf("prompt does not carry the request verbatim:\n%s", got)
	}
	// The fence is the same path as the target, and that is the point:
	// one file is saved, so touching anything else is work thrown away.
	if strings.Count(got, want) < 2 {
		t.Errorf("prompt does not fence writes to the one file:\n%s", got)
	}
	// Both branches have to be in the one text: a session is saved more
	// than once, and the checkout — not the branch, which may lag a save
	// behind — is what says which.
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
	if got, err := (Capture{What: "x"}).Prompt(); err == nil {
		t.Errorf("Prompt rendered a capture with no file to write:\n%s", got)
	}
	if got, err := (Capture{What: "x", Note: "../evil.md"}).Prompt(); err == nil {
		t.Errorf("Prompt rendered a note outside the notes root:\n%s", got)
	}
	if got, err := (Capture{What: "x", Note: "sub/evil.md"}).Prompt(); err == nil {
		t.Errorf("Prompt rendered a note below the notes root:\n%s", got)
	}
}

// The button sends nothing but the click, so the ordinary prompt is the
// one with no request in it at all. It still has to say what to write —
// a template hole here would reach the engine as a blank instruction.
func TestCapturePromptFillsInTheDefaultRequest(t *testing.T) {
	got, err := (Capture{Note: "first-read.md"}).Prompt()
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if !strings.Contains(got, DefaultWhat) {
		t.Errorf("prompt does not carry the default request:\n%s", got)
	}
	if strings.Contains(got, "<no value>") || strings.Contains(got, "{{") {
		t.Errorf("prompt left a template hole:\n%s", got)
	}
}

func TestPendingRoundTrips(t *testing.T) {
	want := Pending{Note: "retry-loop.md", At: time.Now().UTC().Truncate(time.Second)}
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
// exactly this file. They are separate constants in separate modules —
// repo-agent never imports factory — so only this keeps them equal.
func TestNotesRootMatchesFactory(t *testing.T) {
	if NotesRoot != "docs-exploration/research" {
		t.Errorf("NotesRoot = %q; factory's save_notes.sh pushes docs-exploration/research", NotesRoot)
	}
	if got := NotesPath("sess-1.md"); got != "docs-exploration/research/sess-1.md" {
		t.Errorf("NotesPath = %q", got)
	}
}
