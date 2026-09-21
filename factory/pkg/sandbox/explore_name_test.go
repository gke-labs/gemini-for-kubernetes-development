package sandbox

import (
	"strings"
	"testing"
)

func TestExploreSandboxName(t *testing.T) {
	if got := ExploreSandboxName("agent-sandbox"); got != "explore-agent-sandbox" {
		t.Errorf("got %q", got)
	}
	long := strings.Repeat("verylongrepo-", 8)
	got := ExploreSandboxName(long)
	// The companion "<name>-lb" Service must fit a 63-char DNS label.
	if len(got)+len("-lb") > 63 {
		t.Errorf("name %q too long for the -lb Service", got)
	}
	if !strings.HasPrefix(got, "explore-") {
		t.Errorf("got %q", got)
	}
}
