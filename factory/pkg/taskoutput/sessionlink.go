package taskoutput

import (
	"context"
	"regexp"
	"strings"
)

// sessionLinkKey carries the link to the conversation that wrote what an
// apply posts (WithSessionLink).
type sessionLinkKey struct{}

// WithSessionLink is ctx for applies that link what they post on a PR to
// the agent conversation that wrote it: the PR's description (open-pr, a
// rewrite) and its follow-up comments (replies, the report) end with a
// small hint linking link. Empty, nothing is linked.
func WithSessionLink(ctx context.Context, link string) context.Context {
	return context.WithValue(ctx, sessionLinkKey{}, link)
}

// sessionHint is the hint line linking ctx's session, or "".
func sessionHint(ctx context.Context) string {
	link, _ := ctx.Value(sessionLinkKey{}).(string)
	if link == "" {
		return ""
	}
	return "<sub>🤖 [Agent session](" + link + ")</sub>"
}

// withSessionHint is body ending with ctx's hint, if it has one.
func withSessionHint(ctx context.Context, body string) string {
	if hint := sessionHint(ctx); hint != "" {
		return strings.TrimRight(body, "\n") + "\n\n" + hint
	}
	return body
}

// sessionHintRE is a hint line sessionHint wrote, with the blank lines
// before it.
var sessionHintRE = regexp.MustCompile(`\n*<sub>🤖 \[Agent session\]\([^)\s]*\)</sub>[ \t]*`)

// prText is a PR body as compared across a task: without the factory's
// markers and session hint, trimmed.
func prText(body string) string {
	return strings.TrimSpace(sessionHintRE.ReplaceAllString(factoryMarkerRE.ReplaceAllString(body, ""), ""))
}
