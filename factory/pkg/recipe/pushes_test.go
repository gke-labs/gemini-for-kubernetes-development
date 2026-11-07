package recipe

import "testing"

// A recipe with a push step pushes: on a PR it is given the PR's branch
// and head (the pushed_* inputs).
func TestRecipePushes(t *testing.T) {
	for name, want := range map[string]bool{"care-comments": true, "fix": true, "review": false, "triage": false} {
		_, rec, err := Builtin(name)
		if err != nil {
			t.Fatal(err)
		}
		if rec.Pushes() != want {
			t.Errorf("%s pushes: %t, want %t", name, rec.Pushes(), want)
		}
	}
}
