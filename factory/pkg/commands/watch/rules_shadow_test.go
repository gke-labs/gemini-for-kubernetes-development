package watch

import "testing"

// The rules run in shadow when the engine is shadow or on; off or unset
// leaves them off, and settings that do not check out leave the rules off
// altogether rather than stop the watch.
func TestNewRulesControlAndShadow(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		engine, use, canary, off string
		control, shadow          bool
	}{
		{name: "unset"},
		{name: "off", engine: "off"},
		{name: "shadow", engine: "shadow", control: true, shadow: true},
		{name: "on, conflict handed over", engine: "on", use: "conflict=engine", canary: "overseer/engine", control: true, shadow: true},
		{name: "disabled only", off: "review", control: true},
		{name: "an engine it does not know", engine: "maybe"},
		{name: "a rule that is not one", engine: "on", use: "nope=engine"},
		{name: "a use that is not legacy or engine", engine: "on", use: "ci=sometimes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &Watcher{}
			w.RulesEngine, w.RulesUse, w.RulesCanaryLabel, w.RulesDisabled = tc.engine, tc.use, tc.canary, tc.off
			w.rulesControl = w.newRulesControl()
			if got := w.rulesControl != nil; got != tc.control {
				t.Errorf("control %v, want %v", got, tc.control)
			}
			if got := w.newRulesShadow() != nil; got != tc.shadow {
				t.Errorf("shadow %v, want %v", got, tc.shadow)
			}
		})
	}
}
