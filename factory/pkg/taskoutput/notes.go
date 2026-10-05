package taskoutput

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/tasks"
)

// Notes is a Notes document's spec: what a research conversation found,
// in markdown, saved to the member's fork (push-notes).
type Notes struct {
	// Name is the note's file name on the notes branch, without .md: the
	// conversation's title, which whoever keeps the draft may set. Unset,
	// it is the conversation's id.
	Name     string `yaml:"name,omitempty"`
	Markdown string `yaml:"markdown"`
}

// NotesSpec decodes a Notes document's spec.
func (d *Document) NotesSpec() (*Notes, error) {
	if d.Kind != "Notes" {
		return nil, fmt.Errorf("%s task output is not Notes", d.Kind)
	}
	var n Notes
	if err := d.Spec.Decode(&n); err != nil {
		return nil, fmt.Errorf("Notes spec: %w", err)
	}
	if strings.TrimSpace(n.Markdown) == "" {
		return nil, fmt.Errorf("Notes spec has no markdown")
	}
	return &n, nil
}

func parseNotes(raw string) (any, error) {
	md := CleanAgentMarkdown(raw)
	if md == "" {
		return nil, fmt.Errorf("the notes are empty")
	}
	return &Notes{Markdown: md}, nil
}

// noteNameRE is a note's file name: it is a path on a branch people read,
// so a GitHub-style name, which also rules out walking out of the notes
// directory.
var noteNameRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)

// NotePath is where push-notes writes the notes on the notes branch: the
// spec's name, else the conversation's (the session, else the task).
func NotePath(doc *Document, n *Notes) (string, error) {
	name := n.Name
	if name == "" {
		name = doc.Source.Session
	}
	if name == "" {
		name = doc.Source.Task
	}
	name = strings.TrimSuffix(name, ".md")
	if !noteNameRE.MatchString(name) {
		return "", fmt.Errorf("note name %q: want letters, digits, dot, dash or underscore", name)
	}
	return tasks.ResearchNotesDir + "/" + name + ".md", nil
}

// repoTarget is the document's target, which must be a repository.
func repoTarget(doc *Document) (owner, repo string, err error) {
	u, err := url.Parse(doc.Target.URL)
	if err != nil {
		return "", "", fmt.Errorf("target %q: %w", doc.Target.URL, err)
	}
	p := strings.Split(strings.Trim(u.Path, "/"), "/")
	if u.Host != "github.com" || len(p) != 2 || p[0] == "" || p[1] == "" {
		return "", "", fmt.Errorf("%s targets a repository (https://github.com/owner/repo), not %s", doc.Kind, doc.Target.URL)
	}
	return p[0], strings.TrimSuffix(p[1], ".git"), nil
}

// pushTries is how often push-notes builds its commit again when the branch
// moved under it: another conversation saved its notes meanwhile.
const pushTries = 3

// forkWait is how long push-notes waits for a fork it asked for to answer.
var forkWait = 60 * time.Second

// applyPushNotes saves Notes to the notes branch of the caller's fork of the
// target, as one commit adding or replacing the one file. It goes through
// GitHub's API with the caller's token, so the token never reaches the
// sandbox the conversation runs in, and the sandbox need not be up.
func applyPushNotes(ctx context.Context, gh *githubv39.Client, doc *Document, dryRun bool, out io.Writer) error {
	n, err := doc.NotesSpec()
	if err != nil {
		return err
	}
	owner, repo, err := repoTarget(doc)
	if err != nil {
		return err
	}
	path, err := NotePath(doc, n)
	if err != nil {
		return err
	}
	branch := tasks.ResearchNotesBranch
	if dryRun {
		fmt.Fprintf(out, "Would push %s to %s on your fork of %s/%s\n", path, branch, owner, repo)
		return nil
	}
	fork, err := ensureFork(ctx, gh, owner, repo, out)
	if err != nil {
		return err
	}
	content := strings.TrimSpace(n.Markdown) + "\n"
	for try := 1; ; try++ {
		changed, err := commitNote(ctx, gh, fork, repo, branch, path, content)
		if err == nil {
			if changed {
				fmt.Fprintf(out, "Saved %s to %s/%s on %s\n", path, fork, repo, branch)
			} else {
				fmt.Fprintf(out, "No change: %s on %s/%s already has this note\n", branch, fork, repo)
			}
			return nil
		}
		if !moved(err) || try == pushTries {
			return fmt.Errorf("pushing %s to %s/%s: %w", path, fork, repo, err)
		}
	}
}

// ensureFork is the owner of the repository push-notes writes to: the caller's
// fork of owner/repo, made if they have none; the repository itself when
// it is theirs. Whose fork is the token's answer, not a flag: a mistyped
// owner would put a member's notes into somebody else's repository.
func ensureFork(ctx context.Context, gh *githubv39.Client, owner, repo string, out io.Writer) (string, error) {
	user, _, err := gh.Users.Get(ctx, "")
	if err != nil {
		return "", fmt.Errorf("resolving the token's GitHub login: %w", err)
	}
	login := user.GetLogin()
	if strings.EqualFold(login, owner) {
		return owner, nil
	}
	upstream := owner + "/" + repo
	r, _, err := gh.Repositories.Get(ctx, login, repo)
	switch {
	case err == nil:
		if !r.GetFork() || !strings.EqualFold(r.GetParent().GetFullName(), upstream) && !strings.EqualFold(r.GetSource().GetFullName(), upstream) {
			return "", fmt.Errorf("%s/%s exists and is not a fork of %s; not pushing notes to it", login, repo, upstream)
		}
		return login, nil
	case !notFound(err):
		return "", fmt.Errorf("looking for %s/%s: %w", login, repo, err)
	}
	fmt.Fprintf(out, "Forking %s to %s...\n", upstream, login)
	if _, _, err := gh.Repositories.CreateFork(ctx, owner, repo, nil); err != nil {
		var accepted *githubv39.AcceptedError
		if !errors.As(err, &accepted) {
			return "", fmt.Errorf("forking %s: %w", upstream, err)
		}
	}
	// GitHub makes the fork in the background: wait for its default branch
	// to answer, which is what the commit is built from.
	deadline := time.Now().Add(forkWait)
	for {
		r, _, err := gh.Repositories.Get(ctx, login, repo)
		if err == nil {
			if _, _, err := gh.Git.GetRef(ctx, login, repo, "heads/"+r.GetDefaultBranch()); err == nil {
				return login, nil
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("the fork %s/%s did not appear within %s", login, repo, forkWait)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// commitNote writes content to path on branch in owner/repo as one
// commit, and reports whether it changed anything. A branch that doesn't
// exist yet is started with no parent: it carries notes and nothing else,
// so it stays cheap to clone and no code change conflicts with a note.
func commitNote(ctx context.Context, gh *githubv39.Client, owner, repo, branch, path, content string) (bool, error) {
	var parent, baseTree string
	ref, _, err := gh.Git.GetRef(ctx, owner, repo, "heads/"+branch)
	switch {
	case err == nil:
		parent = ref.GetObject().GetSHA()
		c, _, err := gh.Git.GetCommit(ctx, owner, repo, parent)
		if err != nil {
			return false, fmt.Errorf("reading %s: %w", branch, err)
		}
		baseTree = c.GetTree().GetSHA()
		file, _, _, err := gh.Repositories.GetContents(ctx, owner, repo, path, &githubv39.RepositoryContentGetOptions{Ref: branch})
		if err == nil && file != nil {
			if have, err := file.GetContent(); err == nil && have == content {
				return false, nil
			}
		} else if err != nil && !notFound(err) {
			return false, fmt.Errorf("reading %s: %w", path, err)
		}
	case !notFound(err):
		return false, fmt.Errorf("reading %s: %w", branch, err)
	}
	tree, _, err := gh.Git.CreateTree(ctx, owner, repo, baseTree, []*githubv39.TreeEntry{{
		Path:    githubv39.String(path),
		Mode:    githubv39.String("100644"),
		Type:    githubv39.String("blob"),
		Content: githubv39.String(content),
	}})
	if err != nil {
		return false, fmt.Errorf("writing the tree: %w", err)
	}
	commit := &githubv39.Commit{
		Message: githubv39.String(fmt.Sprintf("research(%s): notes", path[strings.LastIndex(path, "/")+1:])),
		Tree:    &githubv39.Tree{SHA: tree.SHA},
	}
	if parent != "" {
		commit.Parents = []*githubv39.Commit{{SHA: githubv39.String(parent)}}
	}
	c, _, err := gh.Git.CreateCommit(ctx, owner, repo, commit)
	if err != nil {
		return false, fmt.Errorf("writing the commit: %w", err)
	}
	next := &githubv39.Reference{Ref: githubv39.String("refs/heads/" + branch), Object: &githubv39.GitObject{SHA: c.SHA}}
	if parent == "" {
		_, _, err = gh.Git.CreateRef(ctx, owner, repo, next)
	} else {
		_, _, err = gh.Git.UpdateRef(ctx, owner, repo, next, false)
	}
	if err != nil {
		return false, &movedError{err}
	}
	return true, nil
}

// movedError is a ref update GitHub refused: the branch is no longer
// where the commit was built from, or another save made it first.
type movedError struct{ err error }

func (e *movedError) Error() string { return "updating the branch: " + e.err.Error() }
func (e *movedError) Unwrap() error { return e.err }

func moved(err error) bool {
	var m *movedError
	if !errors.As(err, &m) {
		return false
	}
	var resp *githubv39.ErrorResponse
	return errors.As(err, &resp) && resp.Response != nil && resp.Response.StatusCode == http.StatusUnprocessableEntity
}

func notFound(err error) bool {
	var resp *githubv39.ErrorResponse
	return errors.As(err, &resp) && resp.Response != nil && resp.Response.StatusCode == http.StatusNotFound
}
