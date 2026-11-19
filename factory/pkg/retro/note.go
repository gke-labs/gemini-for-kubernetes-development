package retro

import (
	"strings"
	"unicode"
)

// NoteCommand starts a note for a per-PR retro (design/retro-per-pr.md,
// Notes): a maintainer's comment on the PR whose text starts with it.
const NoteCommand = "/retro"

// IsNote reports whether a comment's body is a /retro note: its text,
// leading blank space aside, starts with /retro, followed by nothing or
// by blank space. "/retrospective" is not one.
func IsNote(body string) bool {
	_, ok := NoteText(body)
	return ok
}

// NoteText is a note's text without its /retro; false when body is not a
// note (IsNote).
func NoteText(body string) (string, bool) {
	s := strings.TrimLeftFunc(body, unicode.IsSpace)
	if len(s) < len(NoteCommand) || !strings.EqualFold(s[:len(NoteCommand)], NoteCommand) {
		return "", false
	}
	rest := s[len(NoteCommand):]
	if rest != "" {
		if r := []rune(rest)[0]; !unicode.IsSpace(r) {
			return "", false
		}
	}
	return strings.TrimSpace(rest), true
}
