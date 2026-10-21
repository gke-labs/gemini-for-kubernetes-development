package watch

import "testing"

// The engine decides every rule, tallied, whatever the settings: a
// disabled rule that is not one leaves every rule on rather than stop the
// watch.
func TestNewRulesControlAndShadow(t *testing.T) {
	for _, tc := range []struct {
		name, off string
		disabled  []string
	}{
		{name: "unset"},
		{name: "review disabled", off: "review", disabled: []string{"review"}},
		{name: "a rule that is not one", off: "nope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &Watcher{}
			w.RulesDisabled = tc.off
			w.rulesControl = w.newRulesControl()
			if w.rulesControl == nil {
				t.Fatal("no control")
			}
			if got := w.rulesControl.Disabled; len(got) != len(tc.disabled) || (len(got) > 0 && got[0] != tc.disabled[0]) {
				t.Errorf("disabled = %v, want %v", got, tc.disabled)
			}
			if w.newRulesSummary() == nil {
				t.Error("no summary")
			}
		})
	}
}
