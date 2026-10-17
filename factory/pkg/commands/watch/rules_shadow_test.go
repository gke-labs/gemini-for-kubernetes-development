package watch

import "testing"

// The rules run in shadow only when RulesEngine says shadow; off, unset or
// a value the watch does not know leaves them off.
func TestNewRulesShadow(t *testing.T) {
	for engine, want := range map[string]bool{"": false, "off": false, "shadow": true, "on": false} {
		w := &Watcher{}
		w.RulesEngine = engine
		if got := w.newRulesShadow() != nil; got != want {
			t.Errorf("engine %q: shadow %v, want %v", engine, got, want)
		}
	}
}
