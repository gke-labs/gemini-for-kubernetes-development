package retro

// The per-PR retro's staging (design/retro-per-pr.md, What the retro
// reads): one closed PR's outcome record, archived tasks and human
// feedback from the collector, beside what the caller read from GitHub -
// the PR itself, its /retro notes, and its feedback for when the
// collector holds none. The layout, the retro-pr recipe's input contract:
//
//	staged.json                    what was staged (PRStaged), and what was not
//	pr.json                        the PR (PRInfo)
//	outcome.json                   its outcome record, when one is saved
//	feedback.jsonl                 its human feedback, notes left out, when it has any
//	notes.jsonl                    its /retro notes (Note), when it has any
//	tasks/<sandbox>/<task>/        an archived task: manifest.json and its files

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/tokenusage"
)

// Note is a /retro note (IsNote): a maintainer's comment on the PR.
type Note struct {
	Author string    `json:"author"`
	At     time.Time `json:"at"`
	URL    string    `json:"url,omitempty"`
	// Body is the note's text, without its /retro.
	Body string `json:"body"`
}

// PRInfo is the PR a per-PR retro is on, as GitHub has it.
type PRInfo struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	Title  string `json:"title"`
	Body   string `json:"body,omitempty"`
	Author string `json:"author"`
	// State is merged or closed (unmerged).
	State    string    `json:"state"`
	ClosedAt time.Time `json:"closedAt"`
	// Base is the branch it was for; BaseSHA and HeadSHA its last base
	// and head commits. A PR's commits stay readable at
	// refs/pull/<n>/head after its branch is deleted.
	Base    string `json:"base,omitempty"`
	BaseSHA string `json:"baseSha,omitempty"`
	HeadSHA string `json:"headSha,omitempty"`
	// Assignees are its human assignees, pool bots left out.
	Assignees []string `json:"assignees,omitempty"`
}

// PRConfig is one PR's staging.
type PRConfig struct {
	// Collector is the collector's base URL; empty, nothing is read from
	// it, and the retro runs on what the caller gives.
	Collector   string
	Owner, Repo string
	PR          PRInfo
	Notes       []Note
	// Feedback is the PR's human feedback as read from GitHub, staged
	// when the PR has no outcome record: the record's, saved when the PR
	// closed, comes first.
	Feedback []tokenusage.FeedbackItem
	// Dir is where the layout is written; it is created.
	Dir    string
	Client *http.Client
	// MaxFileBytes caps one staged file; 0 is DefaultMaxFileBytes.
	MaxFileBytes int64
}

// PRStaged is a per-PR retro's staged.json: what it has to work with.
type PRStaged struct {
	Kind     string    `json:"kind"` // "pr"
	Repo     string    `json:"repo"`
	PR       int       `json:"pr"`
	URL      string    `json:"url"`
	State    string    `json:"state"`
	StagedAt time.Time `json:"stagedAt"`
	// Outcome is whether the PR's outcome record was saved.
	Outcome bool `json:"outcome"`
	// Tasks are its archived agent tasks, <sandbox>/<task>.
	Tasks []string `json:"tasks,omitempty"`
	// Feedback is how many human feedback items were staged, and
	// FeedbackFrom where from: collector or pr ("" for none).
	Feedback     int    `json:"feedback"`
	FeedbackFrom string `json:"feedbackFrom,omitempty"`
	Notes        int    `json:"notes"`
	// Skipped are what was not staged, with why.
	Skipped []string `json:"skipped,omitempty"`
}

// PRRun is one per-PR retro, as the watch hands it to its runner
// (pkg/commands): the PR and what was read of it from GitHub.
type PRRun struct {
	Owner, Repo string
	PR          PRInfo
	Notes       []Note
	// Feedback is the PR's human feedback from GitHub (PRConfig.Feedback).
	Feedback []tokenusage.FeedbackItem
	// Key names the run - the PR and the label application it answers -
	// and so its sandbox and its task: a run of a key already started is
	// picked up, not started again.
	Key string
	// Labels are what the retro PR is opened with.
	Labels []string
	// User is the pool account the retro started under; when non-empty,
	// a resumed or retried try uses it instead of picking another (its
	// branch is on that account's fork).
	User string
	// Resume says the retro may have started before (a watch restarted
	// under it): its run is picked up, and its sandbox is not the
	// runner's to delete before it has found the run.
	Resume bool
	// Started is told, once, that the retro has started: what it has to
	// work with, a link to its agent session ("" for none), and the pool
	// account it runs under. Not called when there is nothing to retro on.
	Started func(st *PRStaged, session, user string)
}

// ErrTryAgain marks a per-PR retro that failed in a way a later try can
// mend, with nothing to report yet: before its agent started (the
// staging, the sandbox, GitHub), or after its task ended well, in
// applying or reading back its result. The watch tries it again later -
// the run, if it started, is picked up by its key - rather than report a
// failure (TryAgain).
var ErrTryAgain = errors.New("the retro did not finish; trying it again later")

// TryAgain is err, marked as one a later try can mend (ErrTryAgain).
func TryAgain(err error) error {
	if err == nil || errors.Is(err, ErrTryAgain) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrTryAgain, err)
}

// PRResult is how a per-PR retro ended. Staged.Empty() is nothing to
// retro on, and nothing ran; else PRURL is the retro PR it opened, or
// Comment why no instruction should change.
type PRResult struct {
	Staged  *PRStaged
	PRURL   string
	Comment string
	// Session links the retro's agent session; "" for none.
	Session string
}

// Empty reports whether there is nothing to retro on: no outcome record,
// no archived task, no human feedback and no note.
func (s *PRStaged) Empty() bool {
	return !s.Outcome && len(s.Tasks) == 0 && s.Feedback == 0 && s.Notes == 0
}

// Had says what the retro has to work with, and what it has not, for the
// comments on the PR.
func (s *PRStaged) Had() (had, missing []string) {
	add := func(ok bool, yes, no string) {
		if ok {
			had = append(had, yes)
		} else {
			missing = append(missing, no)
		}
	}
	add(len(s.Tasks) > 0, fmt.Sprintf("the agents' traces (%d archived %s)", len(s.Tasks), plural(len(s.Tasks), "task", "tasks")), "the agents' traces")
	add(s.Outcome, "the PR's outcome (care passes, human edits)", "the PR's outcome record")
	add(s.Feedback > 0, fmt.Sprintf("human feedback (%d %s)", s.Feedback, plural(s.Feedback, "item", "items")), "human feedback")
	add(s.Notes > 0, fmt.Sprintf("%d `/retro` %s", s.Notes, plural(s.Notes, "note", "notes")), "`/retro` notes")
	return had, missing
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// PROutcome is PR pr's outcome record from the collector at base; nil
// when none is saved. An error is a collector that did not answer, which
// says nothing either way.
func PROutcome(ctx context.Context, base string, hc *http.Client, owner, repo string, pr int) (*tokenusage.OutcomeRecord, error) {
	c := newClient(base, hc)
	var out struct {
		Outcomes []tokenusage.OutcomeRecord `json:"outcomes"`
	}
	q := url.Values{"repo": {owner + "/" + repo}, "pr": {strconv.Itoa(pr)}}
	if err := c.getJSON(ctx, "/v1/outcomes?"+q.Encode(), &out); err != nil {
		return nil, fmt.Errorf("reading PR #%d's outcome record: %w", pr, err)
	}
	for i := range out.Outcomes {
		if out.Outcomes[i].PR == pr {
			return &out.Outcomes[i], nil
		}
	}
	return nil, nil
}

func newClient(base string, hc *http.Client) client {
	c := client{base: strings.TrimRight(base, "/"), http: hc}
	if c.http == nil {
		c.http = &http.Client{Timeout: 2 * time.Minute}
	}
	return c
}

// StagePR writes one PR's layout to cfg.Dir and returns its summary, also
// written as staged.json. The outcome record, when the collector has one,
// and the PR's task list must read; an individual task file or feedback
// stream that does not is left out and listed as skipped.
func StagePR(ctx context.Context, cfg PRConfig) (*PRStaged, error) {
	if cfg.Owner == "" || cfg.Repo == "" || cfg.Dir == "" || cfg.PR.Number <= 0 {
		return nil, errors.New("a repository, a PR and a directory are needed")
	}
	maxBytes := cfg.MaxFileBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxFileBytes
	}
	repo := cfg.Owner + "/" + cfg.Repo
	pr := cfg.PR.Number
	st := &PRStaged{Kind: "pr", Repo: repo, PR: pr, URL: cfg.PR.URL, State: cfg.PR.State, StagedAt: time.Now().UTC()}
	if err := writeJSON(filepath.Join(cfg.Dir, "pr.json"), cfg.PR); err != nil {
		return nil, err
	}

	var feedback []tokenusage.FeedbackItem
	if cfg.Collector != "" {
		c := newClient(cfg.Collector, cfg.Client)
		outcome, err := PROutcome(ctx, cfg.Collector, c.http, cfg.Owner, cfg.Repo, pr)
		if err != nil {
			return nil, err
		}
		if outcome != nil {
			st.Outcome = true
			if err := writeJSON(filepath.Join(cfg.Dir, "outcome.json"), outcome); err != nil {
				return nil, err
			}
			st.FeedbackFrom = "collector"
			path := fmt.Sprintf("/v1/prs/%s/%s/%d/feedback", url.PathEscape(cfg.Owner), url.PathEscape(cfg.Repo), pr)
			switch items, err := c.feedback(ctx, path, maxBytes); {
			case err == nil:
				feedback = items
			case !errors.Is(err, errNotFound):
				st.Skipped = append(st.Skipped, "feedback.jsonl: "+err.Error())
			}
		}
		tasks, skipped, err := c.stageTasks(ctx, repo, pr, filepath.Join(cfg.Dir, "tasks"), maxBytes, true)
		if err != nil {
			return nil, err
		}
		st.Tasks, st.Skipped = tasks, append(st.Skipped, skipped...)
	}
	if !st.Outcome {
		feedback, st.FeedbackFrom = cfg.Feedback, "pr"
	}
	// A note is a maintainer's comment too, and so feedback: it reaches
	// the retro once, as a note.
	var kept []tokenusage.FeedbackItem
	for _, f := range feedback {
		if !IsNote(f.Body) {
			kept = append(kept, f)
		}
	}
	if st.Feedback = len(kept); st.Feedback > 0 {
		if err := writeJSONL(filepath.Join(cfg.Dir, "feedback.jsonl"), kept); err != nil {
			return nil, err
		}
	} else {
		st.FeedbackFrom = ""
	}
	if st.Notes = len(cfg.Notes); st.Notes > 0 {
		if err := writeJSONL(filepath.Join(cfg.Dir, "notes.jsonl"), cfg.Notes); err != nil {
			return nil, err
		}
	}
	if err := writeJSON(filepath.Join(cfg.Dir, StagedFile), st); err != nil {
		return nil, err
	}
	return st, nil
}

// feedback reads a PR's feedback.jsonl from the collector, item by item.
func (c client) feedback(ctx context.Context, path string, maxBytes int64) ([]tokenusage.FeedbackItem, error) {
	tmp, err := os.CreateTemp("", "feedback-")
	if err != nil {
		return nil, err
	}
	name := tmp.Name()
	tmp.Close()
	defer os.Remove(name)
	if err := c.download(ctx, path, name, maxBytes); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	var items []tokenusage.FeedbackItem
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64<<10), int(maxBytes)+1)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var it tokenusage.FeedbackItem
		if err := json.Unmarshal(line, &it); err != nil {
			return nil, fmt.Errorf("feedback.jsonl: %w", err)
		}
		items = append(items, it)
	}
	return items, sc.Err()
}
