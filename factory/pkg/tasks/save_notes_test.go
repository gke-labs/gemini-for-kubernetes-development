package tasks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The credential is the reason this script exists separately from the
// conversation, so the things that would leak it onto the PVC are
// asserted against directly. A research session can be running with
// approvals turned off: anything written to ${HOME} or into a remote
// URL is readable by the agent afterwards, and a token it can read is
// a token it can push with.
func TestSaveNotesLeavesNoCredentialBehind(t *testing.T) {
	b, err := GetSaveNotesScript()
	if err != nil {
		t.Fatalf("GetSaveNotesScript: %v", err)
	}
	// Comments stripped: the script explains at length which of these
	// things it is avoiding and why, and prose saying "no hosts.yml"
	// must not read as a hosts.yml.
	s := withoutComments(string(b))

	// lib.sh's setupGit writes hosts.yml and a global url.insteadOf
	// rewrite, both carrying the token, both on the PVC. GetSaveNotesScript
	// reads the file raw for this reason; if it ever goes through
	// getScriptWithDefaults the prelude comes with it.
	if strings.Contains(s, "function setupGit") || strings.Contains(s, "hosts.yml") {
		t.Error("the save-notes script carries setupGit; the token would be written to the PVC")
	}
	if strings.Contains(s, "insteadOf") {
		t.Error("the save-notes script configures a url rewrite; that is where the token gets persisted")
	}
	// --global writes to ${HOME}/.gitconfig, which outlives the command.
	if strings.Contains(s, "config --global") {
		t.Error("the save-notes script writes global git config; it must configure only the throwaway clone")
	}
	// A token interpolated into a remote URL lands in .git/config and
	// in the reflog of a clone, and shows up in `ps` while it runs.
	for _, bad := range []string{"${GH_TOKEN}@", "${GITHUB_TOKEN}@", "x-access-token:"} {
		if strings.Contains(s, bad) {
			t.Errorf("the save-notes script puts the token in a URL (%q)", bad)
		}
	}

}

// The shell literals and the Go constants are the same two strings with
// nothing linking them, and repo-agent builds the link members follow
// out of the Go side. Changing one alone puts the notes somewhere the
// panel does not look.
func TestSaveNotesWritesWhereRepoAgentLooks(t *testing.T) {
	b, err := GetSaveNotesScript()
	if err != nil {
		t.Fatalf("GetSaveNotesScript: %v", err)
	}
	s := withoutComments(string(b))

	if !strings.Contains(s, `NOTES_BRANCH="`+ResearchNotesBranch+`"`) {
		t.Errorf("the script does not write to %s", ResearchNotesBranch)
	}
	if !strings.Contains(s, `NOTES_PATH="`+ResearchNotesDir+`/${NOTES_FILE}"`) {
		t.Errorf("the script does not write under %s", ResearchNotesDir)
	}
	// The note is named after the conversation, and the caller is the
	// half that knows what it is called. The id is what is left when
	// nothing does — including for anyone running this by hand.
	if !strings.Contains(s, `NOTES_FILE="${NOTES_FILE:-${SESSION_ID}.md}"`) {
		t.Errorf("the script does not fall back to the session id for its note")
	}
	// Runs are pruned when a run is removed, and carry live teardown
	// scripts. Notes are archival and written by a session that may be
	// approving its own tool calls. They do not share a branch.
	if strings.Contains(s, "research/runs") {
		t.Error("the save-notes script touches the runs branch")
	}
}

// withoutComments drops whole-line shell comments. The script has no
// trailing comments and no '#' inside a string, so nothing subtler is
// needed.
func withoutComments(s string) string {
	var kept []string
	for _, line := range strings.Split(s, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// saveNotesEnv is one fully wired sandbox: a checkout holding a
// session's note, a bare repository standing in for the member's fork,
// and a gh that answers without a network.
type saveNotesEnv struct {
	root, fork, notesDir string
	// note is the file this session writes, relative to notesDir.
	note   string
	env    []string
	script string
}

func newSaveNotesEnv(t *testing.T, session string) *saveNotesEnv {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash required")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git required")
	}
	b, err := GetSaveNotesScript()
	if err != nil {
		t.Fatal(err)
	}
	// The script reads the checkout from /workspaces, which no test can
	// write to. Only that root is redirected — everything the test is
	// actually about runs as shipped. A substitution that stopped
	// matching would leave the script reading the real /workspaces, so
	// the count is checked rather than assumed.
	script := strings.ReplaceAll(string(b), "/workspaces/${REPO_NAME}", "${WORKSPACES}/${REPO_NAME}")
	if strings.Count(string(b), "/workspaces/${REPO_NAME}") == 0 {
		t.Fatal("the script no longer reads the checkout from /workspaces/${REPO_NAME}; update this test")
	}

	root := t.TempDir()
	e := &saveNotesEnv{
		root:     root,
		fork:     filepath.Join(root, "remotes", "alice", "repo.git"),
		notesDir: filepath.Join(root, "workspaces", "repo", "docs-exploration", "research"),
		note:     session + ".md",
		script:   script,
	}
	for _, d := range []string{e.notesDir, filepath.Join(root, "bin"), filepath.Join(root, "home")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// insteadOf sends the https URL the script builds at a local path,
	// so the push is real git against a real remote with no network and
	// no credentials in play. It is handed over as GIT_CONFIG_GLOBAL,
	// which only the caller's environment can set, and lives outside
	// HOME: the script ignores ${HOME}/.gitconfig, because that is where
	// the conversation can write. Every git the test runs uses it, so
	// whatever the developer has in their own ~/.gitconfig stays out.
	gitconfig := "[url \"" + filepath.Join(root, "remotes") + "/\"]\n" +
		"\tinsteadOf = https://github.com/\n" +
		"[init]\n\tdefaultBranch = main\n" +
		// The stand-in fork is a bare repository and the test reads it
		// with -C; safe.bareRepository=explicit refuses that.
		"[safe]\n\tbareRepository = all\n"
	if err := os.WriteFile(filepath.Join(root, "gitconfig"), []byte(gitconfig), 0o644); err != nil {
		t.Fatal(err)
	}
	// gh answers the two questions the script asks it, and nothing else.
	gh := "#!/bin/bash\ncase \"$1 $2\" in\n" +
		"  \"api user\") echo alice ;;\n" +
		"  \"repo fork\") echo \"fork ensured\" ;;\n" +
		"  *) echo \"unexpected gh $*\" >&2; exit 1 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(root, "bin", "gh"), []byte(gh), 0o755); err != nil {
		t.Fatal(err)
	}

	e.env = []string{
		"HOME=" + filepath.Join(root, "home"),
		"GIT_CONFIG_GLOBAL=" + filepath.Join(root, "gitconfig"),
		"PATH=" + filepath.Join(root, "bin") + string(os.PathListSeparator) + os.Getenv("PATH"),
		"WORKSPACES=" + filepath.Join(root, "workspaces"),
		"REPO_NAME=repo",
		"UPSTREAM_REPO=gke-labs/repo",
		"SESSION_ID=" + session,
		"GITHUB_USER_NAME=alice",
		"GITHUB_USER_EMAIL=alice@example.com",
		"GH_TOKEN=not-a-real-token",
	}

	// The fork, as an empty repository: a member who has only held
	// research conversations has a fork with no notes branch on it.
	mustRun(t, root, e.env, "git", "init", "--quiet", "--bare", e.fork)
	return e
}

// forSession is a second conversation in the same sandbox, pushing to
// the same fork.
func (e *saveNotesEnv) forSession(session string) *saveNotesEnv {
	other := *e
	other.note = session + ".md"
	other.env = replaceEnv(e.env, "SESSION_ID="+session)
	return &other
}

// named is the same conversation saving to a file named after it
// rather than after its id — which is what repo-agent asks for, the id
// being the fallback for a session nobody has named.
func (e *saveNotesEnv) named(note string) *saveNotesEnv {
	other := *e
	other.note = note
	other.env = replaceEnv(e.env, "NOTES_FILE="+note)
	return &other
}

// write is the conversation writing its note in the checkout.
func (e *saveNotesEnv) write(t *testing.T, content string) {
	t.Helper()
	e.writeFile(t, e.note, content)
}

// writeFile is anything else the conversation leaves in the notes
// directory of its checkout.
func (e *saveNotesEnv) writeFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.notesDir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (e *saveNotesEnv) save(t *testing.T) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", "-c", e.script)
	cmd.Dir = e.root
	cmd.Env = e.env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// onBranch reads a path out of the fork's notes branch.
func (e *saveNotesEnv) onBranch(t *testing.T, path string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", "-C", e.fork, "show", ResearchNotesBranch+":"+path)
	cmd.Env = e.env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func mustRun(t *testing.T, dir string, env []string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return string(out)
}

// The first save on a fork has no branch to add to. It must create one
// and land the notes on it, or a member who has only ever held research
// conversations can never save anything.
func TestSaveNotesStartsTheBranchOnTheFirstSave(t *testing.T) {
	e := newSaveNotesEnv(t, "s1")
	e.write(t, "# what the retry loop does\n")

	out, err := e.save(t)
	if err != nil {
		t.Fatalf("save: %v\n%s", err, out)
	}
	got, gerr := e.onBranch(t, "docs-exploration/research/s1.md")
	if gerr != nil {
		t.Fatalf("reading the branch: %v\n%s", gerr, got)
	}
	if got != "# what the retry loop does\n" {
		t.Errorf("notes on the branch = %q", got)
	}

	// Prose and nothing else. The branch is built from an empty
	// repository rather than off the default branch so that cloning it
	// stays cheap however large the repository is — a code file here
	// means it was branched off the checkout instead.
	tree := mustRun(t, e.root, e.env, "git", "-C", e.fork, "ls-tree", "-r", "--name-only", ResearchNotesBranch)
	for _, line := range strings.Split(strings.TrimSpace(tree), "\n") {
		if !strings.HasPrefix(line, "docs-exploration/research/") {
			t.Errorf("the notes branch carries %q; it should hold notes only", line)
		}
	}
}

// A second save has to update its own note and add to the branch
// rather than replace it — every other conversation's note is on there
// too, and none of them is this session's business.
func TestSaveNotesUpdatesItsOwnNoteAndKeepsTheRest(t *testing.T) {
	e := newSaveNotesEnv(t, "s1")
	e.write(t, "first\n")
	if out, err := e.save(t); err != nil {
		t.Fatalf("first save: %v\n%s", err, out)
	}

	// Another session's note, already on the branch.
	other := e.forSession("s2")
	other.write(t, "someone else's reading\n")
	if out, err := other.save(t); err != nil {
		t.Fatalf("second session: %v\n%s", err, out)
	}

	e.write(t, "second\n")
	if out, err := e.save(t); err != nil {
		t.Fatalf("re-save: %v\n%s", err, out)
	}

	if got, _ := e.onBranch(t, "docs-exploration/research/s1.md"); got != "second\n" {
		t.Errorf("s1.md = %q, want the re-saved text", got)
	}
	if got, _ := e.onBranch(t, "docs-exploration/research/s2.md"); got != "someone else's reading\n" {
		t.Errorf("another session's note was disturbed: %q", got)
	}
}

// One file is saved, and it is the one the caller named. A session that
// scribbled something else into the notes directory of its checkout —
// or that wandered into another conversation's note there — does not
// get it pushed.
func TestSaveNotesPushesNothingButTheNoteItWasGiven(t *testing.T) {
	e := newSaveNotesEnv(t, "s1")
	e.write(t, "ours\n")
	e.writeFile(t, "scratch.md", "a thought that did not survive\n")
	e.writeFile(t, "s2.md", "another conversation's note\n")

	if out, err := e.save(t); err != nil {
		t.Fatalf("save: %v\n%s", err, out)
	}
	for _, stray := range []string{"scratch.md", "s2.md"} {
		if _, err := e.onBranch(t, "docs-exploration/research/"+stray); err == nil {
			t.Errorf("%s rode along onto the branch", stray)
		}
	}
}

// The file the note lands in is the caller's to name: it is the note's
// address on a branch someone reads months later, and a session id is
// not an address.
func TestSaveNotesUsesTheNameItIsGiven(t *testing.T) {
	e := newSaveNotesEnv(t, "s1").named("where-the-retry-loop-terminates.md")
	e.write(t, "# what the retry loop does\n")

	if out, err := e.save(t); err != nil {
		t.Fatalf("save: %v\n%s", err, out)
	}
	got, gerr := e.onBranch(t, "docs-exploration/research/where-the-retry-loop-terminates.md")
	if gerr != nil {
		t.Fatalf("reading the branch: %v\n%s", gerr, got)
	}
	if got != "# what the retry loop does\n" {
		t.Errorf("the note on the branch = %q", got)
	}
	if _, err := e.onBranch(t, "docs-exploration/research/s1.md"); err == nil {
		t.Error("the note also landed under the session id")
	}
}

// Saving the same notes twice must not pile up empty commits, and must
// not report failure either — the member pressed the button, and
// nothing changing is a fine outcome.
func TestSaveNotesIsQuietWhenNothingChanged(t *testing.T) {
	e := newSaveNotesEnv(t, "s1")
	e.write(t, "unchanged\n")
	if out, err := e.save(t); err != nil {
		t.Fatalf("first save: %v\n%s", err, out)
	}
	before := mustRun(t, e.root, e.env, "git", "-C", e.fork, "rev-parse", ResearchNotesBranch)

	out, err := e.save(t)
	if err != nil {
		t.Fatalf("second save: %v\n%s", err, out)
	}
	if !strings.Contains(out, "No change") {
		t.Errorf("second save did not report that nothing changed:\n%s", out)
	}
	if after := mustRun(t, e.root, e.env, "git", "-C", e.fork, "rev-parse", ResearchNotesBranch); after != before {
		t.Error("an empty commit was pushed")
	}
}

// The conversation can write ${HOME}/.gitconfig, and git runs here with
// the member's token in its environment. A hook it planted there — or in
// the throwaway clone's own config, which outranks the global one — must
// not run.
func TestSaveNotesRunsNoHookTheConversationPlanted(t *testing.T) {
	e := newSaveNotesEnv(t, "s1")
	hooks := filepath.Join(e.root, "planted-hooks")
	marker := filepath.Join(e.root, "hook-ran")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	hook := "#!/bin/sh\necho \"$GH_TOKEN\" > " + marker + "\n"
	for _, name := range []string{"pre-commit", "pre-push", "post-checkout"} {
		if err := os.WriteFile(filepath.Join(hooks, name), []byte(hook), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	planted := "[core]\n\thooksPath = " + hooks + "\n"
	if err := os.WriteFile(filepath.Join(e.root, "home", ".gitconfig"), []byte(planted), 0o644); err != nil {
		t.Fatal(err)
	}
	// The same path in the caller's own global config stands for a
	// repository-level setting: the script must win over that too.
	f, err := os.OpenFile(filepath.Join(e.root, "gitconfig"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(planted); err != nil {
		t.Fatal(err)
	}
	f.Close()

	e.write(t, "notes\n")
	if out, err := e.save(t); err != nil {
		t.Fatalf("save: %v\n%s", err, out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a planted hook ran while the token was in the environment")
	}
}

// Nothing to save is a failure, not a quiet success. The caller asked
// the conversation to write the note and then asked for it to be saved;
// reporting success would send a member to a branch with nothing on it.
func TestSaveNotesFailsWhenTheConversationWroteNothing(t *testing.T) {
	e := newSaveNotesEnv(t, "s1")

	out, err := e.save(t)
	if err == nil {
		t.Fatalf("save of a missing note succeeded:\n%s", out)
	}
	if !strings.Contains(out, "has not written its note") {
		t.Errorf("the failure does not say what went wrong:\n%s", out)
	}
	// An empty file is the same nothing: a turn that opened the note and
	// wrote no prose into it has not written the note.
	e.write(t, "")
	if out, err := e.save(t); err == nil {
		t.Fatalf("save of an empty note succeeded:\n%s", out)
	}
	// And it must not have created the branch on the way to finding out.
	if err := exec.Command("git", "-C", e.fork, "rev-parse", ResearchNotesBranch).Run(); err == nil {
		t.Error("an empty save still created the notes branch")
	}
}

// Another session pushing between our clone and our push must not cost
// us the summary the member waited for.
func TestSaveNotesReplaysOntoAConcurrentPush(t *testing.T) {
	e := newSaveNotesEnv(t, "s1")
	e.write(t, "ours\n")
	if out, err := e.save(t); err != nil {
		t.Fatalf("seed save: %v\n%s", err, out)
	}

	// The other session's commit, parked on a ref of its own for now.
	// Pushing it to the notes branch here would only test a fast-forward:
	// the script clones after that push and never notices. It has to land
	// in the window between our clone and our push.
	side := filepath.Join(e.root, "side")
	mustRun(t, e.root, e.env, "git", "clone", "--quiet", "--branch", ResearchNotesBranch, e.fork, side)
	mustRun(t, side, e.env, "git", "config", "user.name", "bob")
	mustRun(t, side, e.env, "git", "config", "user.email", "bob@example.com")
	if err := os.MkdirAll(filepath.Join(side, "docs-exploration", "research"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(side, "docs-exploration", "research", "s3.md"), []byte("theirs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, side, e.env, "git", "add", "-A")
	mustRun(t, side, e.env, "git", "commit", "--quiet", "-m", "theirs")
	mustRun(t, side, e.env, "git", "push", "--quiet", "origin", "HEAD:refs/heads/bobs-version")

	// A hook closes the window by hand: the first push to the notes
	// branch finds that bob got there first and is rejected, which is
	// exactly what the remote does to a real racing save.
	hook := "#!/bin/bash\n" +
		"if [ -f hooks/.fired ]; then exit 0; fi\n" +
		"touch hooks/.fired\n" +
		"env -u GIT_QUARANTINE_PATH -u GIT_OBJECT_DIRECTORY -u GIT_ALTERNATE_OBJECT_DIRECTORIES \\\n" +
		"  git update-ref refs/heads/" + ResearchNotesBranch + " refs/heads/bobs-version\n" +
		"echo 'someone else saved first' >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(e.fork, "hooks", "pre-receive"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}

	e.write(t, "ours, revised\n")
	out, err := e.save(t)
	if err != nil {
		t.Fatalf("save after a concurrent push: %v\n%s", err, out)
	}
	if !strings.Contains(out, "replayed onto it") {
		t.Errorf("the save did not go through the replay path:\n%s", out)
	}
	if got, _ := e.onBranch(t, "docs-exploration/research/s1.md"); got != "ours, revised\n" {
		t.Errorf("our note = %q, want the revision", got)
	}
	if got, _ := e.onBranch(t, "docs-exploration/research/s3.md"); got != "theirs\n" {
		t.Errorf("the concurrent session's note was lost: %q", got)
	}
}

func replaceEnv(env []string, kv string) []string {
	key := strings.SplitN(kv, "=", 2)[0] + "="
	out := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, key) {
			out = append(out, e)
		}
	}
	return append(out, kv)
}
