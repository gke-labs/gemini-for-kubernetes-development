// Package retro is the weekly retro's staging (design/retro.md §6): the
// week's outcome records, rollups, and the picked PRs' feedback and
// archived tasks, read from the token-usage collector into a directory -
// the layout the retro recipe reads from its task's staged/.
//
// The layout, the recipe's input contract:
//
//	staged.json                        the repository, the window, the picks and why
//	outcomes.jsonl                     the window's outcome records, one per line
//	rollups/skill.json                 the per-skill rollups
//	rollups/workflow.json              the per-workflow rollups
//	prs/<n>/feedback.jsonl             a picked PR's human feedback, when it has any
//	prs/<n>/tasks/<sandbox>/<task>/    a picked PR's archived task: its manifest.json
//	                                   (the task's index record) and its files
package retro

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/tokenusage"
)

// StagedFile is the layout's summary file.
const StagedFile = "staged.json"

// minCarePasses and minHumanEditLines are when a PR's care passes, or the
// lines a human changed after the agent's last push, make it worth a look.
const (
	minCarePasses     = 3
	minHumanEditLines = 20
)

// DefaultMaxFileBytes is the largest archived file staged; a larger one is
// listed as skipped.
const DefaultMaxFileBytes = 64 << 20

// Config is what to stage, from where, to where.
type Config struct {
	// Collector is the collector's base URL (COLLECTOR_URL).
	Collector   string
	Owner, Repo string
	// Since and Until bound the window, by when a PR closed; a zero Until
	// is now.
	Since, Until time.Time
	// Dir is where the layout is written; it is created.
	Dir    string
	Client *http.Client
	// MaxFileBytes caps one staged file; 0 is DefaultMaxFileBytes.
	MaxFileBytes int64
}

// Staged is staged.json.
type Staged struct {
	Repo     string    `json:"repo"`
	Since    time.Time `json:"since"`
	Until    time.Time `json:"until"`
	StagedAt time.Time `json:"stagedAt"`
	// Outcomes is how many outcome records the window has.
	Outcomes int `json:"outcomes"`
	// PRs are the picked PRs.
	PRs []PickedPR `json:"prs"`
}

// PickedPR is a PR worth a look, what was staged of it and why.
type PickedPR struct {
	PR       int      `json:"pr"`
	State    string   `json:"state"`
	Reasons  []string `json:"reasons"`
	Feedback bool     `json:"feedback"`
	Tasks    []string `json:"tasks,omitempty"` // <sandbox>/<task>
	// Skipped are the files not staged, with why.
	Skipped []string `json:"skipped,omitempty"`
}

// Reasons is why an outcome is worth a look: closed unmerged, many care
// passes, human feedback, or human edits after the agent's last push.
// None, it is not picked.
func Reasons(o tokenusage.OutcomeRecord) []string {
	var out []string
	if o.State == "closed" {
		out = append(out, "closed unmerged")
	}
	if o.CarePasses >= minCarePasses {
		out = append(out, fmt.Sprintf("%d care passes", o.CarePasses))
	}
	if f := o.Feedback; f != nil && f.Items > 0 {
		out = append(out, fmt.Sprintf("%d human feedback items", f.Items))
	}
	if e := o.HumanEdit; e != nil && e.Added+e.Deleted >= minHumanEditLines {
		out = append(out, fmt.Sprintf("human edits: %d files, +%d -%d", e.Files, e.Added, e.Deleted))
	}
	return out
}

// Stage writes the layout to cfg.Dir and returns its summary, also written
// as staged.json. The window's records and rollups must read; a picked PR's
// feedback or file that does not is left out, and a file that does not is
// listed as skipped.
func Stage(ctx context.Context, cfg Config) (*Staged, error) {
	if cfg.Collector == "" {
		return nil, errors.New("no collector URL (COLLECTOR_URL)")
	}
	if cfg.Owner == "" || cfg.Repo == "" || cfg.Dir == "" {
		return nil, errors.New("a repository and a directory are needed")
	}
	c := client{base: strings.TrimRight(cfg.Collector, "/"), http: cfg.Client}
	if c.http == nil {
		c.http = &http.Client{Timeout: 2 * time.Minute}
	}
	maxBytes := cfg.MaxFileBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxFileBytes
	}
	now := time.Now().UTC()
	until := cfg.Until
	if until.IsZero() {
		until = now
	}
	repo := cfg.Owner + "/" + cfg.Repo
	st := &Staged{Repo: repo, Since: cfg.Since.UTC(), Until: until.UTC(), StagedAt: now, PRs: []PickedPR{}}

	window := url.Values{"repo": {repo}, "since": {cfg.Since.UTC().Format(time.RFC3339)}, "until": {until.UTC().Format(time.RFC3339)}}
	var outcomes struct {
		Outcomes []tokenusage.OutcomeRecord `json:"outcomes"`
	}
	if err := c.getJSON(ctx, "/v1/outcomes?"+window.Encode(), &outcomes); err != nil {
		return nil, fmt.Errorf("reading outcome records: %w", err)
	}
	st.Outcomes = len(outcomes.Outcomes)
	if err := writeJSONL(filepath.Join(cfg.Dir, "outcomes.jsonl"), outcomes.Outcomes); err != nil {
		return nil, err
	}
	for _, by := range []string{"skill", "workflow"} {
		var r struct {
			Rollups []tokenusage.OutcomeRollup `json:"rollups"`
		}
		q := url.Values{"repo": {repo}, "since": {cfg.Since.UTC().Format(time.RFC3339)}, "by": {by}}
		if err := c.getJSON(ctx, "/v1/outcomes/rollup?"+q.Encode(), &r); err != nil {
			return nil, fmt.Errorf("reading the %s rollups: %w", by, err)
		}
		if r.Rollups == nil {
			r.Rollups = []tokenusage.OutcomeRollup{}
		}
		if err := writeJSON(filepath.Join(cfg.Dir, "rollups", by+".json"), r.Rollups); err != nil {
			return nil, err
		}
	}

	for _, o := range outcomes.Outcomes {
		reasons := Reasons(o)
		if len(reasons) == 0 {
			continue
		}
		pick := PickedPR{PR: o.PR, State: o.State, Reasons: reasons}
		prDir := filepath.Join(cfg.Dir, "prs", strconv.Itoa(o.PR))
		fb := fmt.Sprintf("/v1/prs/%s/%s/%d/feedback", url.PathEscape(cfg.Owner), url.PathEscape(cfg.Repo), o.PR)
		switch err := c.download(ctx, fb, filepath.Join(prDir, "feedback.jsonl"), maxBytes); {
		case err == nil:
			pick.Feedback = true
		case !errors.Is(err, errNotFound):
			pick.Skipped = append(pick.Skipped, "feedback.jsonl: "+err.Error())
		}

		var tasks struct {
			Tasks []tokenusage.TaskRecord `json:"tasks"`
		}
		q := url.Values{"repo": {repo}, "pr": {strconv.Itoa(o.PR)}}
		if err := c.getJSON(ctx, "/v1/tasks?"+q.Encode(), &tasks); err != nil {
			pick.Skipped = append(pick.Skipped, "tasks: "+err.Error())
		}
		for _, t := range tasks.Tasks {
			id := t.Sandbox + "/" + t.Task
			dir, err := within(filepath.Join(prDir, "tasks"), t.Sandbox, t.Task)
			if err != nil {
				pick.Skipped = append(pick.Skipped, id+": "+err.Error())
				continue
			}
			if err := writeJSON(filepath.Join(dir, "manifest.json"), t); err != nil {
				return nil, err
			}
			pick.Tasks = append(pick.Tasks, id)
			for _, f := range t.Files {
				dest, err := within(dir, f.Name)
				switch {
				case err != nil:
					pick.Skipped = append(pick.Skipped, id+"/"+f.Name+": "+err.Error())
					continue
				case f.Bytes > maxBytes:
					pick.Skipped = append(pick.Skipped, fmt.Sprintf("%s/%s: %d bytes, over %d", id, f.Name, f.Bytes, maxBytes))
					continue
				}
				p := fmt.Sprintf("/v1/tasks/%s/%s/files/%s", url.PathEscape(t.Sandbox), url.PathEscape(t.Task), escapePath(f.Name))
				if err := c.download(ctx, p, dest, maxBytes); err != nil {
					pick.Skipped = append(pick.Skipped, id+"/"+f.Name+": "+err.Error())
				}
			}
		}
		st.PRs = append(st.PRs, pick)
	}
	if err := writeJSON(filepath.Join(cfg.Dir, StagedFile), st); err != nil {
		return nil, err
	}
	return st, nil
}

var errNotFound = errors.New("not found")

type client struct {
	base string
	http *http.Client
}

func (c client) get(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return resp, nil
	case http.StatusNotFound:
		resp.Body.Close()
		return nil, errNotFound
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	resp.Body.Close()
	return nil, fmt.Errorf("GET %s: %s: %s", path, resp.Status, strings.TrimSpace(string(b)))
}

func (c client) getJSON(ctx context.Context, path string, v any) error {
	resp, err := c.get(ctx, path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}

// download writes path's body to dest, at most maxBytes of it.
func (c client) download(ctx context.Context, path, dest string, maxBytes int64) error {
	resp, err := c.get(ctx, path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxBytes+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > maxBytes {
		err = fmt.Errorf("over %d bytes", maxBytes)
	}
	if err != nil {
		_ = os.Remove(dest)
	}
	return err
}

// within is root joined with parts, refused if it would leave root.
func within(root string, parts ...string) (string, error) {
	rel := filepath.Join(parts...)
	if rel == "" || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe path %q", filepath.Join(parts...))
	}
	return filepath.Join(root, rel), nil
}

// escapePath escapes each segment of a slash-separated file name.
func escapePath(name string) string {
	segs := strings.Split(name, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func writeJSONL[T any](path string, items []T) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	for _, it := range items {
		if err := enc.Encode(it); err != nil {
			f.Close()
			return err
		}
	}
	return f.Close()
}
