package prs

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

// With a recorder, every evaluation is recorded: the facts and what the
// scanner did, in the rules' names. The rules, replayed on the records,
// decide the same.
func TestEvaluate_RecordsDecisionsTheRulesReplay(t *testing.T) {
	no := false
	for _, tc := range []struct {
		name string
		f    *prFixture
		want string
	}{
		{"green, no review wanted: ready", &prFixture{num: 10, headSHA: "sha-1", updated: time.Now()}, "ready run"},
		{"checks pending: nothing", &prFixture{num: 11, headSHA: "sha-1", updated: time.Now(), pending: true}, ""},
		{"conflicting: rebase", &prFixture{num: 12, headSHA: "sha-1", updated: time.Now(), mergeable: &no}, "conflict run"},
		{"already ready: nothing", &prFixture{num: 13, headSHA: "sha-1", updated: time.Now(), readyForHuman: true}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "decisions.jsonl")
			s, _ := newTestScanner(t, t.TempDir(), testOpts{
				GitHub: tc.f.start(t), BotUsers: []string{"bot1"}, GitHubLogin: "bot1", TriggerLabel: "factory",
				Recorder: rules.NewRecorder(path, 0),
			})
			tc.f.mu.Lock()
			listed := tc.f.listingIssueLocked()
			tc.f.mu.Unlock()
			s.evaluate(context.Background(), listed)

			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var rec rules.Record
			if err := json.Unmarshal(bytes.TrimSpace(data), &rec); err != nil {
				t.Fatalf("%v: %s", err, data)
			}
			if got := strings.Join(rec.Decided, ","); got != tc.want || rec.Gate != "" || rec.Scanner != "prs" {
				t.Fatalf("recorded %q (gate %q), want %q: %+v", got, rec.Gate, tc.want, rec.Item)
			}
			set, err := rules.Builtin()
			if err != nil {
				t.Fatal(err)
			}
			res, err := set.Replay(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			if res.Compared != 1 || len(res.Diffs) != 0 {
				for _, d := range res.Diffs {
					t.Errorf("rules %v, scanner %v, item %+v", d.Engine, d.Scanner, d.Record.Item)
				}
			}
		})
	}
}
