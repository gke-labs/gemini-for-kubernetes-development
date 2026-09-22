package conventions

import "strings"

// announcementMarker tags a comment as the watcher talking about itself.
//
// It is an HTML comment so that GitHub renders nothing for it, which lets the
// tag be carried by comments a human is meant to read.
const announcementMarker = "<!-- factory:announcement -->"

// legacyAnnouncementPrefix recognises the announcements posted before the
// marker existed. Every one of them opens by naming the work being started, so
// the prefix identifies the family without pinning any single wording.
//
// It exists for the pull requests that were already open when the marker was
// introduced, whose threads hold untagged announcements that will never be
// rewritten.
const legacyAnnouncementPrefix = "🤖 AI Factory started "

// Announce tags a comment as an announcement: something the watcher is saying
// about its own activity, rather than a reply to anybody.
//
// The distinction matters because the scanner treats a comment from its own
// account as evidence that the feedback above it has been dealt with. That is
// right for a reply and wrong for an announcement - "I have started work" is
// the opposite of an answer - so announcements say so explicitly.
func Announce(body string) string {
	if body == "" {
		return ""
	}
	return body + "\n\n" + announcementMarker
}

// IsAnnouncement reports whether a comment body is one of the watcher's
// announcements about its own activity.
func IsAnnouncement(body string) bool {
	if strings.Contains(body, announcementMarker) {
		return true
	}
	return strings.HasPrefix(strings.TrimSpace(body), legacyAnnouncementPrefix)
}
