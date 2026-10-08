package taskoutput

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	githubv39 "github.com/google/go-github/v39/github"
)

const fanOutSpec = "## Fan-out\n```yaml\ntitle: \"Migrate {{.item.name}}\"\n```\n\n## Task\nMigrate {{.item.name}}.\n\n## Items\n- [ ] **A** (a.go)\n- [ ] B\n"

// fanOutGitHub is an issue's labels and comments, as the bot sees them.
type fanOutGitHub struct {
	labels   []string
	comments []githubv39.IssueComment
	edits    int
}

func (f *fanOutGitHub) client(t *testing.T) *githubv39.Client {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"login": "bot"}`)
	})
	mux.HandleFunc("GET /repos/o/r/issues/12", func(w http.ResponseWriter, r *http.Request) {
		var labels []map[string]string
		for _, l := range f.labels {
			labels = append(labels, map[string]string{"name": l})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"number": 12, "labels": labels})
	})
	mux.HandleFunc("POST /repos/o/r/issues/12/labels", func(w http.ResponseWriter, r *http.Request) {
		var l []string
		_ = json.NewDecoder(r.Body).Decode(&l)
		f.labels = append(f.labels, l...)
		fmt.Fprint(w, "[]")
	})
	mux.HandleFunc("GET /repos/o/r/issues/12/comments", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(f.comments)
	})
	mux.HandleFunc("POST /repos/o/r/issues/12/comments", func(w http.ResponseWriter, r *http.Request) {
		var c githubv39.IssueComment
		_ = json.NewDecoder(r.Body).Decode(&c)
		c.ID = githubv39.Int64(int64(len(f.comments) + 1))
		c.User = &githubv39.User{Login: githubv39.String("bot")}
		f.comments = append(f.comments, c)
		fmt.Fprint(w, "{}")
	})
	mux.HandleFunc("PATCH /repos/o/r/issues/comments/{id}", func(w http.ResponseWriter, r *http.Request) {
		var c githubv39.IssueComment
		_ = json.NewDecoder(r.Body).Decode(&c)
		for i := range f.comments {
			if fmt.Sprint(f.comments[i].GetID()) == r.PathValue("id") {
				f.comments[i].Body = c.Body
				f.edits++
			}
		}
		fmt.Fprint(w, "{}")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(srv.URL + "/")
	return gh
}

func fanOutDoc(t *testing.T, task, raw string) *Document {
	t.Helper()
	doc, err := Wrap("FanOut", raw, Target{URL: "https://github.com/o/r/issues/12"}, Source{Task: task, Recipe: "fanout"})
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestFanOutWrap(t *testing.T) {
	// The agent's fence and a marker it copied are taken off.
	doc := fanOutDoc(t, "t", "```markdown\n<!-- factory:fanout-spec -->\n"+fanOutSpec+"```")
	f, err := doc.FanOutSpec()
	if err != nil || !strings.HasPrefix(f.Markdown, "## Fan-out\n") {
		t.Fatalf("FanOutSpec() = %+v, %v", f, err)
	}
	for _, tc := range []struct{ name, raw, want string }{
		{"no sections", "Fan this out.", "## Task"},
		{"a broken template", "## Task\nDo {{.item.nme}.\n\n## Items\n- [ ] a\n", "does not parse"},
	} {
		if _, err := Wrap("FanOut", tc.raw, Target{}, Source{}); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: Wrap() error = %v, want it to mention %q", tc.name, err, tc.want)
		}
	}
	var verbs []string
	for _, a := range doc.Offered() {
		verbs = append(verbs, a.Verb)
	}
	if strings.Join(verbs, ",") != "edit,post-spec,reject" {
		t.Errorf("actions = %v", verbs)
	}
}

func TestApplyPostSpec(t *testing.T) {
	ctx := context.Background()
	f := &fanOutGitHub{labels: []string{"robot/fanout"}}
	gh := f.client(t)
	var out bytes.Buffer

	// A dry run writes nothing.
	if err := Apply(ctx, gh, fanOutDoc(t, "recipe-fanout-1", fanOutSpec), true, &out); err != nil {
		t.Fatal(err)
	}
	if len(f.comments) != 0 || len(f.labels) != 1 || !strings.Contains(out.String(), "Would add robot/stop") {
		t.Fatalf("dry run: comments %d, labels %v, said:\n%s", len(f.comments), f.labels, out.String())
	}

	// Posted: the stop label for the issue's fanout label, then the spec.
	if err := Apply(ctx, gh, fanOutDoc(t, "recipe-fanout-1", fanOutSpec), false, &out); err != nil {
		t.Fatal(err)
	}
	if len(f.comments) != 1 || !strings.HasPrefix(f.comments[0].GetBody(), "<!-- factory:fanout-spec -->\n## Fan-out") || strings.Join(f.labels, ",") != "robot/fanout,robot/stop" {
		t.Fatalf("comments %q, labels %v", f.comments, f.labels)
	}

	// Started by a maintainer who edited the spec: applying the same task
	// again leaves both alone.
	f.labels = []string{"robot/fanout"}
	f.comments[0].Body = githubv39.String(f.comments[0].GetBody() + "\n- [ ] C")
	if err := Apply(ctx, gh, fanOutDoc(t, "recipe-fanout-1", fanOutSpec), false, &out); err != nil {
		t.Fatal(err)
	}
	if len(f.labels) != 1 || f.edits != 0 || !strings.HasSuffix(f.comments[0].GetBody(), "- [ ] C") {
		t.Errorf("applied again: labels %v, edits %d", f.labels, f.edits)
	}

	// Another task's spec replaces the bot's comment, and stops the fan-out.
	if err := Apply(ctx, gh, fanOutDoc(t, "recipe-fanout-2", strings.Replace(fanOutSpec, "- [ ] B", "- [ ] D", 1)), false, &out); err != nil {
		t.Fatal(err)
	}
	if len(f.comments) != 1 || f.edits != 1 || !strings.Contains(f.comments[0].GetBody(), "- [ ] D") || !strings.Contains(f.comments[0].GetBody(), "task=recipe-fanout-2") {
		t.Errorf("a second task: comments %q, edits %d", f.comments, f.edits)
	}

	// An issue that is no fan-out yet becomes one, stopped.
	f = &fanOutGitHub{labels: []string{"bug"}}
	if err := Apply(ctx, f.client(t), fanOutDoc(t, "recipe-fanout-3", fanOutSpec), false, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.labels, ",") != "bug,overseer/fanout,overseer/stop" || len(f.comments) != 1 {
		t.Errorf("a new fan-out: labels %v, comments %d", f.labels, len(f.comments))
	}
}

func TestFanOutLabels(t *testing.T) {
	label := func(names ...string) []*githubv39.Label {
		var out []*githubv39.Label
		for _, n := range names {
			out = append(out, &githubv39.Label{Name: githubv39.String(n)})
		}
		return out
	}
	for want, labels := range map[string][]*githubv39.Label{
		"overseer/fanout,overseer/stop": label("bug"),
		"kcc-bot/stop":                  label("bug", "kcc-bot/fanout"),
	} {
		if got := strings.Join(fanOutLabels(labels), ","); got != want {
			t.Errorf("fanOutLabels(%v) = %q, want %q", labels, got, want)
		}
	}
}
