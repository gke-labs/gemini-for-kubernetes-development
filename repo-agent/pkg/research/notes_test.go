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

// The note's name comes from the session's title, and it ends up as a
// file name in a pushed tree.
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
// name: the save pushes it under NotesRoot.
func TestNoteNameAlwaysProducesOneFileName(t *testing.T) {
	for _, title := range []string{
		"notes", "../../etc/passwd", "a/b/c", "  spaced  out  ", "CRD_v2",
		"note;rm -rf /", strings.Repeat("long", 40), "über-naïve", "", "…",
	} {
		note := NoteName(title, "s1")
		if strings.ContainsAny(note, "/\\") || strings.Contains(note, "..") || note != strings.TrimSpace(note) {
			t.Errorf("NoteName(%q) = %q, which is not one file name", title, note)
		}
		if !strings.HasSuffix(note, ".md") {
			t.Errorf("NoteName(%q) = %q, want a .md file", title, note)
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
