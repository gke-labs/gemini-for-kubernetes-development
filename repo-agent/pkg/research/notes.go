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
	"fmt"
	"strings"
)

// NotesRoot is where a session's notes live on the notes branch of the
// member's fork, one file per session.
const NotesRoot = "docs-exploration/research"

// NoteAnnotation is the file this session's notes are saved under,
// decided at the first save and kept until the session is renamed.
//
// Pinned rather than derived each time so that saving twice in a row
// updates one file instead of writing a second copy of it. A rename
// drops the pin: the name the member just chose is the name they expect
// the note to be saved under, and the note already on the branch under
// the old name stays there.
const NoteAnnotation = "sandbox.gemini.google.com/research-note"

// NoteLimit is how long a note's file name may be, extension aside.
const NoteLimit = 64

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
