// Package recipe runs a task as a list of steps in one sandbox, around one
// agent session — the shape of a GitHub Actions job with one more kind of
// step:
//
//	uses: a named step that ships with factory (setup-git, setup-repo …).
//	      The only steps that run with the GitHub token.
//	run:  inline shell, run with the agent's privileges: no GitHub token.
//	ask:  a prompt turn. Every ask goes to the same session, so a later
//	      turn does not repeat what an earlier one said.
//
// Steps run in order and the first failure ends the recipe. A recipe is
// data, shipped by the CLI; the runner is the factory binary in the
// sandbox image, which also decides what each `uses` name means.
//
// Values reach steps the way they reach a workflow's: a run step sees its
// inputs as INPUT_<NAME> environment variables and never as shell source,
// so an issue title cannot become a command. An ask is a text/template
// rendered just before the turn is sent, so it can read the inputs and
// what earlier steps produced.
package recipe

import (
	"embed"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"
	"text/template"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/acpd"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskoutput"

	"k8s.io/apimachinery/pkg/api/resource"
)

// WarmRecipe is the built-in recipe that warms a workspace disk for
// snapshots (design/warm-workspace.md). factory runs it in the
// repository's warm sandbox rather than a research one.
const WarmRecipe = "warm"

// AgentRecipe is the built-in recipe a chore (an .agents/ definition
// that is not a workflow) runs as. `factory agent create` runs it on the
// repository in the agent's own sandbox, rather than a research one.
const AgentRecipe = "agent"

// WorkflowRecipe is the built-in recipe a workflow (an .agents/
// definition with mode: workflow) takes its next step as, on its issue.
const WorkflowRecipe = "workflow"

// RunRecipe is the built-in recipe a run's phase runs as, run-plan,
// run-deploy or run-teardown: `factory run <phase>` runs it in the run's
// own sandbox.
func RunRecipe(phase string) string { return "run-" + phase }

// Recipe is one task.
type Recipe struct {
	Name string `yaml:"name"`
	// Label is the recipe's button text; unset, its name capitalised.
	// Like On it is factory's.
	Label string `yaml:"label,omitempty"`
	// On is what the recipe runs on: OnIssue, OnPR, OnMyPR and OnRepo.
	// factory refuses any other target, and a board offers the recipe on
	// those rows. Unset, it runs on any.
	On []string `yaml:"on,omitempty"`
	// Context is the standing rules: what the agent may and may not do.
	// It is sent once, ahead of the first ask, rather than repeated in
	// every turn.
	Context string `yaml:"context,omitempty"`
	// AgentEnv is more of the agent's environment, which its shell
	// commands read: each value a template of the inputs (a workflow's
	// SESSION_ID: "{{ .Inputs.session_id }}"); one that renders empty is
	// left unset. Facts, never credentials: acpd refuses those, and its
	// own variables (acpd.CheckEnv).
	AgentEnv map[string]string `yaml:"agent-env,omitempty"`
	// Inputs declares what the recipe takes beyond what its caller always
	// sets, so a missing or misspelt one fails before a sandbox is made.
	Inputs map[string]Input `yaml:"inputs,omitempty"`
	// Start is what runs when the task is launched, wherever the recipe
	// runs.
	Start Part `yaml:"start"`
	// Outputs are the task-directory files `recipe run` prints when the
	// recipe ends, in order. Unset, they are the capture files.
	Outputs []string `yaml:"outputs,omitempty"`
	// RawTaskOutputs holds the unmarshaled task outputs (single or sequence).
	RawTaskOutputs rawTaskOutputs `yaml:"task-output,omitempty"`
	// TaskOutput declares the task's result: the runner wraps the file the
	// agent wrote into taskoutput.File, which `factory apply` acts on. It
	// is factory's, not the runner's: ForSandbox takes it out of the
	// recipe a sandbox gets, and the task carries it in task.json.
	TaskOutput *taskoutput.Decl `yaml:"-"`
	// TaskOutputs holds all task-output declarations for recipes with multiple outputs.
	TaskOutputs []*taskoutput.Decl `yaml:"-"`
	// TaskType makes the recipe its sandbox's main task, of that type, as
	// plan or fix are: it records its state in last-task-*, where callers
	// already look for that type. Unset, it is a side task,
	// recipe-<name>, beside them. Like TaskOutput it is factory's.
	TaskType string `yaml:"task-type,omitempty"`
	// Credentials is how far the GitHub token reaches: CredentialsFull
	// (unset) or CredentialsClone. Unlike task-type it is the runner's,
	// which enforces it, so it reaches the sandbox.
	Credentials string `yaml:"credentials,omitempty"`
	// As is who a watch runs the recipe's task as, of factory's roles
	// (config roles, an overseer's spec.roles): AsAuthor, the PR's own bot
	// (the pool bot assigned to it, else its author: a recipe that pushes
	// to the PR's branch, in that bot's fork); or a pool - coder, agent,
	// reviewer - pinned to the item. What it does decides it, not the rule
	// that runs it. Every built-in declares it, and so must a repository's
	// recipe (which may say only author or coder). A runner without pools
	// (the board, which runs as its member) does not read it. Like
	// TaskOutput it is factory's.
	As string `yaml:"as,omitempty"`
	// Session is a tag (design/recipe-sessions.md): recipes with the same
	// one, on one target, run into one conversation in its sandbox, and a
	// board shows them as one group. A run continues the conversation the
	// tag's last run recorded, or opens one. Nothing else is shared: each
	// recipe in it is whole. Unset, the recipe keeps a conversation of its
	// own. Like TaskOutput it is factory's.
	Session string `yaml:"session,omitempty"`
	// ApplicableWhen narrows the items on: names to those the recipe
	// applies to. Unset, all of them. Like TaskOutput it is factory's.
	ApplicableWhen *ApplicableWhen `yaml:"applicable-when,omitempty"`
	// Sandbox is the sandbox a recipe on a repository runs in, required
	// for one (SandboxDecl). Like TaskOutput it is factory's.
	Sandbox *SandboxDecl `yaml:"sandbox,omitempty"`

	// Source is where the recipe was loaded from, set by whoever loads it
	// and never part of the file: SourceBuiltin, a local file's path, or
	// a repository's .agents/recipes/<name>.yaml@<commit>.
	Source string `yaml:"-"`
	// Timeout is how long a run may take (a Go duration: 3h), for
	// whoever runs the recipe for a board; unset, its own default. Like
	// TaskOutput it is factory's.
	Timeout string `yaml:"timeout,omitempty"`
}

// A recipe on a repository declares its sandbox: its own, which it names
// and sizes - one per --sandbox-key, <prefix>-<repo>-<short hash of the
// key>, shared by runs of any recipe with the same prefix and key (a
// research session's, a run's) - or one of the special kinds below.
const (
	// SandboxWarm is the repository's warm workspace (design/warm-
	// workspace.md): one per repository, no secret mounted, never restored
	// from the snapshot it makes, sized by the caller, and not a board's.
	SandboxWarm = "warm"
	// SandboxAgent is an agent's own, which factory agent create makes;
	// factory recipe does not run a recipe of this kind.
	SandboxAgent = "agent"
)

// SourceBuiltin is a built-in recipe's Source.
const SourceBuiltin = "builtin"

// sandboxPrefixRE is a prefix a recipe may give its sandboxes' names.
var sandboxPrefixRE = regexp.MustCompile(`^[a-z][a-z0-9]{0,9}$`)

// SandboxDecl is the sandbox a recipe on a repository runs in: a special
// kind (warm, agent), or its own, which the recipe names (prefix) and
// sizes in full - nothing else sizes it, not factory's code nor the
// caller.
type SandboxDecl struct {
	// Kind is a special kind, warm or agent; empty for the recipe's own.
	Kind string `yaml:"kind,omitempty"`
	// Prefix starts the sandbox's name.
	Prefix           string `yaml:"prefix,omitempty"`
	Disk             string `yaml:"disk,omitempty"`
	EphemeralStorage string `yaml:"ephemeral-storage,omitempty"`
	CPURequest       string `yaml:"cpu-request,omitempty"`
	CPULimit         string `yaml:"cpu-limit,omitempty"`
	MemoryRequest    string `yaml:"memory-request,omitempty"`
	MemoryLimit      string `yaml:"memory-limit,omitempty"`
}

// Own reports whether the recipe runs in a sandbox of its own (prefix,
// named by --sandbox-key) rather than a special kind's.
func (d *SandboxDecl) Own() bool { return d != nil && d.Kind == "" }

// sizes are the size fields, by their YAML names.
func (d *SandboxDecl) sizes() map[string]string {
	return map[string]string{"disk": d.Disk, "ephemeral-storage": d.EphemeralStorage, "cpu-request": d.CPURequest,
		"cpu-limit": d.CPULimit, "memory-request": d.MemoryRequest, "memory-limit": d.MemoryLimit}
}

// ApplicableWhen is which items a recipe applies to: factory refuses to
// run it on others, and a board offers it only on these.
type ApplicableWhen struct {
	// Label is a label the item must have. It is a template of the trigger
	// label, as the watch spells its labels: "{{ .Trigger }}/fanout" is
	// overseer/fanout or <trigger>/fanout (Labels).
	Label string `yaml:"label" json:"label"`
}

// Labels are the labels Label names, one of which the item must have:
// Label rendered for each of triggers, without repeats.
func (a *ApplicableWhen) Labels(triggers ...string) ([]string, error) {
	tmpl, err := template.New("label").Option("missingkey=error").Parse(a.Label)
	if err != nil {
		return nil, fmt.Errorf("applicable-when label %q: %w", a.Label, err)
	}
	var out []string
	for _, trigger := range triggers {
		var b strings.Builder
		if err := tmpl.Execute(&b, struct{ Trigger string }{trigger}); err != nil {
			return nil, fmt.Errorf("applicable-when label %q: %w", a.Label, err)
		}
		label := strings.TrimSpace(b.String())
		if label != "" && !slices.ContainsFunc(out, func(l string) bool { return strings.EqualFold(l, label) }) {
			out = append(out, label)
		}
	}
	return out, nil
}

// SessionName is the conversation the recipe's runs are recorded under:
// its session tag, or its own name.
func (r *Recipe) SessionName() string {
	if r.Session != "" {
		return r.Session
	}
	return r.Name
}

// What a recipe runs on.
const (
	OnIssue = "issue"
	// OnPR is any pull request: the recipe reads it, and writes only
	// through applies with the caller's token.
	OnPR = "pr"
	// OnMyPR is a pull request the caller may push to: authored by them,
	// its head a branch on their fork, the only place push pushes to.
	OnMyPR = "my-pr"
	OnRepo = "repo"
)

var onTargets = []string{OnIssue, OnPR, OnMyPR, OnRepo}

// DisplayLabel is Label, or the name capitalised.
func (r *Recipe) DisplayLabel() string {
	if r.Label != "" {
		return r.Label
	}
	return strings.ToUpper(r.Name[:1]) + r.Name[1:]
}

const (
	// CredentialsFull: every uses step runs with the token, and setup-git
	// leaves it in gh's hosts.yml for the agent's gh to use.
	CredentialsFull = "full"
	// CredentialsClone: the token reaches the clone step and nothing else.
	// The caller hands it over outside the task's environment (the task's
	// secrets), the runner gives it to that one step, which runs before
	// the first ask, and forgets it; nothing writes it to disk. For a
	// session nobody approves (yolo), which could read back anything the
	// sandbox holds.
	CredentialsClone = "clone"
	// CloneStep is the named step that clones the repository with the
	// token in its environment alone.
	CloneStep = "clone"
)

// fullCredentialSteps are the named steps that put the token where the
// agent can reach it later — on disk, or in the checkout's remote — or
// write with it: setup-fork may fork, push pushes.
var fullCredentialSteps = []string{"setup-git", "setup-repo", "setup-fork", "checkout-default-branch", "checkout-pr-branch", "push", "runs-remote", "run-prepare", "run-finish"}

// Part is a list of steps run in order: the recipe's start.
type Part struct {
	Steps []Step `yaml:"steps"`
}

// RunsOn reports whether the recipe runs on target (On; unset, any): a
// my-pr is a PR too.
func (r *Recipe) RunsOn(target string) bool {
	return len(r.On) == 0 || slices.Contains(r.On, target) || (target == OnMyPR && slices.Contains(r.On, OnPR))
}

// Input is one declared input.
type Input struct {
	Description string `yaml:"description,omitempty"`
	Default     string `yaml:"default,omitempty"`
	Required    bool   `yaml:"required,omitempty"`
	// Type is "" for a string, or InstructionsType. Like task-output it is
	// factory's: ForSandbox drops it.
	Type string `yaml:"type,omitempty"`
	// From is a task output kind the input takes: a result of that kind
	// whose actions offer `run: <this recipe>` starts it with its draft
	// (markdown, or spec as YAML) as this input. Factory's: ForSandbox
	// drops it.
	From string `yaml:"from,omitempty"`
}

// InputFrom is the input that takes a kind's result, the one a `run:
// <recipe>` action on it fills.
func (r *Recipe) InputFrom(kind string) (string, bool) {
	for _, name := range slices.Sorted(maps.Keys(r.Inputs)) {
		if r.Inputs[name].From == kind {
			return name, true
		}
	}
	return "", false
}

// InstructionsType is an input given any number of times on the command
// line, each a local file, a file in the repository or the text itself;
// the recipe gets them joined into one string.
const InstructionsType = "instructions"

// Step is one step; exactly one of Uses, Run and Ask is set.
type Step struct {
	// ID names the step for later ones: {{ .Steps.<id>.ExitCode }}.
	ID string `yaml:"id,omitempty"`
	// If, when set, is a template rendered as an ask is (inputs and the
	// steps before), to true or false: false skips the step.
	If   string            `yaml:"if,omitempty"`
	Uses string            `yaml:"uses,omitempty"`
	With map[string]string `yaml:"with,omitempty"`
	Run  string            `yaml:"run,omitempty"`
	Ask  string            `yaml:"ask,omitempty"`
	// Capture writes an ask's reply to this file in the task directory,
	// where the CLI reads it back, as runEngine's output file is today.
	Capture string `yaml:"capture,omitempty"`
	// Expects is the file in the task directory an ask tells the agent to
	// write ({{ output "<file>" }}): after the ask it must be there and
	// not empty. An agent that forgot is asked once more, then the step
	// fails, before a later step (a push) acts on a result that is not
	// there.
	Expects string `yaml:"expects,omitempty"`
	// ContinueOnError lets a failed run step be judged by a later one
	// instead of ending the recipe.
	ContinueOnError bool `yaml:"continue-on-error,omitempty"`
}

// Kind is "uses", "run" or "ask".
func (s Step) Kind() string {
	switch {
	case s.Uses != "":
		return "uses"
	case s.Run != "":
		return "run"
	default:
		return "ask"
	}
}

// Label names the step in the log.
func (s Step) Label(i int) string {
	if s.ID != "" {
		return s.ID
	}
	switch s.Kind() {
	case "uses":
		return fmt.Sprintf("%d:%s", i+1, s.Uses)
	default:
		return fmt.Sprintf("%d:%s", i+1, s.Kind())
	}
}

// NamedSteps maps each `uses` name to the lib.sh function it runs. A
// closed set: these are the steps that hold the GitHub token, so a recipe
// can choose among them but cannot add one.
var NamedSteps = map[string]string{
	"setup-git":               "setupGit",
	"setup-repo":              "setupGitRepos",
	"setup-fork":              "setupForkRepo",
	"checkout-default-branch": "checkoutDefaultBranch",
	"checkout-pr-branch":      "checkoutPRBranch",
	"configure-engine":        "configureGemini",
	"push":                    "pushToFork",
	"runs-remote":             "setupRunsRemote",
	"run-prepare":             "runPrepare",
	"run-finish":              "runFinish",
	"ci-logs":                 "fetchPRCILogs",
	"pr-comments":             "fetchPRComments",
	"issue-comments":          "fetchIssueComments",
	"keyed-comment":           "fetchKeyedComment",
	CloneStep:                 "cloneRepo",
}

var (
	stepIDRE   = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	inputRE    = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	taskTypeRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	withKeyRE  = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
)

// Parse reads and validates a recipe.
func Parse(data []byte) (*Recipe, error) {
	return parse(data, false)
}

// ParseKept parses a recipe a task kept when it started (its
// recipe-source.yaml) without its sandbox: - the sandbox is made by then,
// and whatever shape an older factory gave it is not this one's to judge.
func ParseKept(data []byte) (*Recipe, error) {
	data, err := withoutSandbox(data)
	if err != nil {
		return nil, fmt.Errorf("parsing recipe: %w", err)
	}
	return parse(data, true)
}

// withoutSandbox is a recipe's YAML with its sandbox: key taken out.
func withoutSandbox(data []byte) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return data, nil
	}
	m := doc.Content[0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == "sandbox" {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return yaml.Marshal(&doc)
		}
	}
	return data, nil
}

// rawTaskOutputs unmarshals task-output as either a single Decl or a sequence of Decls.
type rawTaskOutputs []*taskoutput.Decl

func (t *rawTaskOutputs) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.SequenceNode {
		var list []*taskoutput.Decl
		if err := n.Decode(&list); err != nil {
			return err
		}
		*t = list
		return nil
	}
	var single taskoutput.Decl
	if err := n.Decode(&single); err != nil {
		return err
	}
	*t = []*taskoutput.Decl{&single}
	return nil
}

func (t rawTaskOutputs) MarshalYAML() (any, error) {
	if len(t) == 1 {
		return t[0], nil
	}
	return []*taskoutput.Decl(t), nil
}

func parse(data []byte, kept bool) (*Recipe, error) {
	var r Recipe
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("parsing recipe: %w", err)
	}
	if len(r.RawTaskOutputs) > 0 {
		r.TaskOutputs = r.RawTaskOutputs
		r.TaskOutput = r.RawTaskOutputs[0]
	}
	if err := r.validate(!kept); err != nil {
		return nil, err
	}
	return &r, nil
}

// The roles a recipe runs as (Recipe.As).
const (
	AsAuthor   = "author"
	AsCoder    = "coder"
	AsAgent    = "agent"
	AsReviewer = "reviewer"
)

// validateAs holds a recipe's as: one of the roles, author only for a
// recipe on pull requests alone.
func (r *Recipe) validateAs() error {
	switch r.As {
	case "", AsCoder, AsAgent, AsReviewer:
		return nil
	case AsAuthor:
		for _, on := range r.On {
			if on != OnPR && on != OnMyPR {
				return fmt.Errorf("recipe %s: as author is for a recipe on pull requests, not on %s", r.Name, on)
			}
		}
		if len(r.On) == 0 {
			return fmt.Errorf("recipe %s: as author is for a recipe on pull requests (on: [pr])", r.Name)
		}
		return nil
	default:
		return fmt.Errorf("recipe %s: as %q is not one of: %s, %s, %s, %s", r.Name, r.As, AsAuthor, AsCoder, AsAgent, AsReviewer)
	}
}

// validateSandbox holds a recipe's sandbox: required for a recipe on a
// repository and only for one; a special kind declares nothing else, the
// recipe's own its prefix and every size, as quantities.
func (r *Recipe) validateSandbox() error {
	onRepo := len(r.On) == 1 && r.On[0] == OnRepo
	sb := r.Sandbox
	switch {
	case sb == nil && onRepo:
		return fmt.Errorf("recipe %s: a recipe on: [repo] declares its sandbox: (prefix and sizes, or kind %s or %s)", r.Name, SandboxWarm, SandboxAgent)
	case sb == nil:
		return nil
	case !onRepo:
		return fmt.Errorf("recipe %s: sandbox: is for a recipe on: [repo]", r.Name)
	}
	if sb.Kind != "" {
		if sb.Kind != SandboxWarm && sb.Kind != SandboxAgent {
			return fmt.Errorf("recipe %s: sandbox kind %q is not one of: %s, %s", r.Name, sb.Kind, SandboxWarm, SandboxAgent)
		}
		for field, v := range sb.sizes() {
			if v != "" {
				return fmt.Errorf("recipe %s: a %s sandbox declares no %s", r.Name, sb.Kind, field)
			}
		}
		if sb.Prefix != "" {
			return fmt.Errorf("recipe %s: a %s sandbox declares no prefix", r.Name, sb.Kind)
		}
		return nil
	}
	if !sandboxPrefixRE.MatchString(sb.Prefix) {
		return fmt.Errorf("recipe %s: sandbox prefix %q is not %s", r.Name, sb.Prefix, sandboxPrefixRE)
	}
	for _, field := range []string{"cpu-request", "cpu-limit", "memory-request", "memory-limit", "ephemeral-storage", "disk"} {
		v := sb.sizes()[field]
		if v == "" {
			return fmt.Errorf("recipe %s: the sandbox declares no %s: a recipe's sandbox is sized by the recipe alone", r.Name, field)
		}
		if _, err := resource.ParseQuantity(v); err != nil {
			return fmt.Errorf("recipe %s: sandbox %s %q: %w", r.Name, field, v, err)
		}
	}
	return nil
}

// Validate checks what can be checked before anything runs, so a bad
// recipe fails in the CLI and not halfway through a sandbox.
func (r *Recipe) Validate() error {
	return r.validate(true)
}

// validate is Validate, holding the sandbox: or not (a kept recipe's).
func (r *Recipe) validate(sandbox bool) error {
	if r.Name == "" {
		return fmt.Errorf("recipe has no name")
	}
	if err := r.validateAs(); err != nil {
		return err
	}
	if sandbox {
		if err := r.validateSandbox(); err != nil {
			return err
		}
	}
	if len(r.Start.Steps) == 0 {
		return fmt.Errorf("recipe %s has no start steps", r.Name)
	}
	if err := acpd.CheckEnv(r.AgentEnv); err != nil {
		return fmt.Errorf("agent-env: %w", err)
	}
	for k, v := range r.AgentEnv {
		if _, err := template.New(k).Option("missingkey=error").Funcs(templateFuncs("")).Parse(v); err != nil {
			return fmt.Errorf("agent-env %s: %w", k, err)
		}
	}
	for name, in := range r.Inputs {
		if !inputRE.MatchString(name) {
			return fmt.Errorf("input %q must match %s", name, inputRE)
		}
		if in.Required && in.Default != "" {
			return fmt.Errorf("input %s: required and default are exclusive", name)
		}
		if in.Type != "" && in.Type != InstructionsType {
			return fmt.Errorf("input %s: type %q is not one of: %s", name, in.Type, InstructionsType)
		}
		if in.From != "" {
			if !taskoutput.Known(in.From) {
				return fmt.Errorf("input %s: from %q is not one of: %s", name, in.From, strings.Join(taskoutput.KnownKinds(), ", "))
			}
			if in.Required || in.Type != "" {
				return fmt.Errorf("input %s: an input from a result is an optional string", name)
			}
			if other, _ := r.InputFrom(in.From); other != name {
				return fmt.Errorf("inputs %s and %s both take a %s", other, name, in.From)
			}
		}
	}
	for _, on := range r.On {
		if !slices.Contains(onTargets, on) {
			return fmt.Errorf("on %q is not one of: %s", on, strings.Join(onTargets, ", "))
		}
	}
	if r.TaskType != "" && !taskTypeRE.MatchString(r.TaskType) {
		return fmt.Errorf("task-type %q must match %s", r.TaskType, taskTypeRE)
	}
	if r.Session != "" {
		switch {
		case !taskTypeRE.MatchString(r.Session):
			return fmt.Errorf("session %q must match %s", r.Session, taskTypeRE)
		case r.TaskType != "":
			return fmt.Errorf("a recipe in a session is recorded under it; it takes no task-type")
		}
	}
	if aw := r.ApplicableWhen; aw != nil {
		if labels, err := aw.Labels("overseer"); err != nil {
			return err
		} else if len(labels) == 0 {
			return fmt.Errorf("applicable-when needs a label")
		}
	}
	for _, to := range r.TaskOutputs {
		if !taskoutput.Known(to.Kind) {
			return fmt.Errorf("task-output kind %q is not one of: %s", to.Kind, strings.Join(taskoutput.KnownKinds(), ", "))
		}
		if !safeFileName(to.From) {
			return fmt.Errorf("task-output from %q must be a plain file name", to.From)
		}
		if err := taskoutput.ValidateActions(to.Kind, to.Actions); err != nil {
			return fmt.Errorf("task-output: %w", err)
		}
		if to.Preview != "" && !taskoutput.PreviewRE.MatchString(to.Preview) {
			return fmt.Errorf("task-output: preview %q must name a spec field, such as spec.body", to.Preview)
		}
		if c := to.Comment; c != nil && c.Key != "" && !taskoutput.CommentKeyRE.MatchString(c.Key) {
			return fmt.Errorf("task-output: comment key %q is not lowercase letters, digits and dashes, starting with a letter", c.Key)
		}
		for _, a := range to.Actions {
			if a.Verb == "run" && !takesFrom(r, a.Run, to.Kind) {
				return fmt.Errorf("task-output: action run %s: recipe %s has no input from a %s", a.Run, a.Run, to.Kind)
			}
		}
		if to.OnFinish != "" {
			if class, _ := taskoutput.VerbClass(to.OnFinish); class != taskoutput.ClassApply {
				return fmt.Errorf("task-output: on-finish %q must be an apply action, such as open-pr", to.OnFinish)
			}
			offered := to.Actions
			if len(offered) == 0 {
				offered = taskoutput.DefaultActions(to.Kind)
			}
			if !slices.ContainsFunc(offered, func(a taskoutput.Action) bool { return a.Verb == to.OnFinish }) {
				return fmt.Errorf("task-output: on-finish %s is not one of the %s's actions", to.OnFinish, to.Kind)
			}
		}
	}
	if r.Timeout != "" {
		if d, err := time.ParseDuration(r.Timeout); err != nil || d <= 0 {
			return fmt.Errorf("timeout %q must be a positive duration, such as 45m", r.Timeout)
		}
	}
	for _, o := range r.Outputs {
		if !safeFileName(o) {
			return fmt.Errorf("output %q must be a plain file name", o)
		}
	}
	if err := validateSteps(r.Start.Steps); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	if err := r.validateCredentials(); err != nil {
		return err
	}
	return nil
}

// validateCredentials holds a credentials: clone recipe to its rule: one
// clone, in start, before the first ask, and none of the steps that leave
// the token behind.
func (r *Recipe) validateCredentials() error {
	switch r.Credentials {
	case "", CredentialsFull:
		return nil
	case CredentialsClone:
	default:
		return fmt.Errorf("credentials %q is not one of: %s, %s", r.Credentials, CredentialsFull, CredentialsClone)
	}
	for i, s := range r.Start.Steps {
		if slices.Contains(fullCredentialSteps, s.Uses) {
			return fmt.Errorf("start: step %d: %s leaves the GitHub token behind; credentials: clone allows only %s", i+1, s.Uses, CloneStep)
		}
	}
	clones, asked := 0, false
	for i, s := range r.Start.Steps {
		switch {
		case s.Uses == CloneStep && asked:
			return fmt.Errorf("start: step %d: %s must come before the first ask, so no agent runs while the token is held", i+1, CloneStep)
		case s.Uses == CloneStep:
			clones++
		case s.Kind() == "ask":
			asked = true
		}
	}
	if clones != 1 {
		return fmt.Errorf("start: credentials: clone needs one %s step, not %d", CloneStep, clones)
	}
	return nil
}

// validateSteps checks one part's steps. Step ids are the part's own:
// {{ .Steps.<id> }} reaches steps of the same part only.
func validateSteps(steps []Step) error {
	ids := map[string]bool{}
	for i, s := range steps {
		set := 0
		for _, v := range []string{s.Uses, s.Run, s.Ask} {
			if v != "" {
				set++
			}
		}
		if set != 1 {
			return fmt.Errorf("step %d: exactly one of uses, run and ask must be set", i+1)
		}
		if s.ID != "" {
			if !stepIDRE.MatchString(s.ID) {
				return fmt.Errorf("step %d: id %q must match %s", i+1, s.ID, stepIDRE)
			}
			if ids[s.ID] {
				return fmt.Errorf("step %d: duplicate id %q", i+1, s.ID)
			}
			ids[s.ID] = true
		}
		if s.Uses != "" {
			if _, ok := NamedSteps[s.Uses]; !ok {
				return fmt.Errorf("step %d: unknown step %q (known: %s)", i+1, s.Uses, strings.Join(namedStepNames(), ", "))
			}
		}
		if len(s.With) > 0 && s.Uses == "" {
			return fmt.Errorf("step %d: with is only for uses steps", i+1)
		}
		for k := range s.With {
			if !withKeyRE.MatchString(k) {
				return fmt.Errorf("step %d: with key %q must match %s", i+1, k, withKeyRE)
			}
		}
		if s.Capture != "" {
			if s.Ask == "" {
				return fmt.Errorf("step %d: capture is only for ask steps", i+1)
			}
			if !safeFileName(s.Capture) {
				return fmt.Errorf("step %d: capture %q must be a plain file name", i+1, s.Capture)
			}
		}
		if s.Expects != "" {
			if s.Ask == "" {
				return fmt.Errorf("step %d: expects is only for ask steps", i+1)
			}
			if !safeFileName(s.Expects) {
				return fmt.Errorf("step %d: expects %q must be a plain file name", i+1, s.Expects)
			}
			if s.Capture != "" {
				return fmt.Errorf("step %d: an ask either captures its reply or expects a file the agent writes, not both", i+1)
			}
		}
		if s.ContinueOnError && s.Run == "" {
			return fmt.Errorf("step %d: continue-on-error is only for run steps", i+1)
		}
		if s.If != "" {
			if _, err := template.New("if").Option("missingkey=error").Funcs(templateFuncs("")).Parse(s.If); err != nil {
				return fmt.Errorf("step %d: if: %w", i+1, err)
			}
		}
	}
	return nil
}

// ResolveInputs is what the recipe runs with: the caller's standard
// inputs, the declared defaults, then overrides. An override must name a
// standard or a declared input, and a required input must end up set.
func (r *Recipe) ResolveInputs(standard, overrides map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range standard {
		out[k] = v
	}
	for name, in := range r.Inputs {
		if _, ok := out[name]; !ok {
			out[name] = in.Default
		}
	}
	for k, v := range overrides {
		if _, ok := out[k]; !ok {
			return nil, fmt.Errorf("recipe %s has no input %q (it takes: %s)", r.Name, k, strings.Join(sortedKeys(out), ", "))
		}
		out[k] = v
	}
	for name, in := range r.Inputs {
		if in.Required && out[name] == "" {
			return nil, fmt.Errorf("recipe %s needs input %s: pass --input %s=…", r.Name, name, name)
		}
	}
	return out, nil
}

// OutputDecl is the task output a task of the recipe declares: a copy of
// TaskOutput, or nil without one.
func (r *Recipe) OutputDecl() *taskoutput.Decl {
	if r.TaskOutput == nil {
		return nil
	}
	decl := *r.TaskOutput
	return &decl
}

// OutputDecls is all task outputs a task of the recipe declares: copies of
// TaskOutputs, or nil without one.
func (r *Recipe) OutputDecls() []*taskoutput.Decl {
	if len(r.TaskOutputs) == 0 {
		if r.TaskOutput != nil {
			return []*taskoutput.Decl{r.OutputDecl()}
		}
		return nil
	}
	var out []*taskoutput.Decl
	for _, d := range r.TaskOutputs {
		cp := *d
		out = append(out, &cp)
	}
	return out
}

// OutputFiles is what `recipe run` prints: Outputs, or else every file an
// ask in start captures, in step order.
func (r *Recipe) OutputFiles() []string {
	if len(r.Outputs) > 0 {
		return r.Outputs
	}
	var out []string
	for _, s := range r.Start.Steps {
		if s.Capture != "" {
			out = append(out, s.Capture)
		}
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func namedStepNames() []string {
	names := make([]string, 0, len(NamedSteps))
	for n := range NamedSteps {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// safeFileName is one name in the task directory: no path, no dotfile.
func safeFileName(name string) bool {
	return name != "" && !strings.ContainsAny(name, `/\`) && !strings.HasPrefix(name, ".")
}

// ForSandbox is the recipe as a sandbox's runner gets it: without label,
// on, task-output, task-type, session, applicable-when, sandbox, timeout
// and input types and sources, which runners older than them reject as unknown
// fields.
func ForSandbox(data []byte) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing recipe: %w", err)
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return data, nil
	}
	m := doc.Content[0]
	changed := false
	for _, k := range []string{"label", "on", "task-output", "task-type", "session", "applicable-when", "sandbox", "timeout", "as"} {
		if dropKey(m, k) {
			changed = true
		}
	}
	if inputs := mapValue(m, "inputs"); inputs != nil && inputs.Kind == yaml.MappingNode {
		for i := 1; i < len(inputs.Content); i += 2 {
			if inputs.Content[i].Kind != yaml.MappingNode {
				continue
			}
			for _, k := range []string{"type", "from"} {
				if dropKey(inputs.Content[i], k) {
					changed = true
				}
			}
		}
	}
	if !changed {
		return data, nil
	}
	return yaml.Marshal(&doc)
}

func dropKey(m *yaml.Node, key string) bool {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return true
		}
	}
	return false
}

func mapValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

//go:embed recipes/*.yaml
var builtinFS embed.FS

// BuiltinNames lists the recipes that ship with factory.
func BuiltinNames() []string {
	entries, _ := builtinFS.ReadDir("recipes")
	var names []string
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".yaml"); ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// takesFrom reports whether recipe name, r itself or a built-in, has an
// input from a kind's result. A built-in is read, not validated: two
// recipes may follow each other.
func takesFrom(r *Recipe, name, kind string) bool {
	follow := r
	if name != r.Name {
		data, err := builtinFS.ReadFile("recipes/" + name + ".yaml")
		if err != nil {
			return false
		}
		follow = &Recipe{}
		if err := yaml.Unmarshal(data, follow); err != nil {
			return false
		}
	}
	_, ok := follow.InputFrom(kind)
	return ok
}

// CheckSessions holds recipes that share a session tag to what makes
// them one group: they agree on what they run on and on their
// credentials, so they land in one sandbox, and one of them is named
// like the tag, the group's default.
func CheckSessions(recs []*Recipe) error {
	groups := map[string][]*Recipe{}
	var tags []string
	for _, r := range recs {
		if r.Session == "" {
			continue
		}
		if _, ok := groups[r.Session]; !ok {
			tags = append(tags, r.Session)
		}
		groups[r.Session] = append(groups[r.Session], r)
	}
	sort.Strings(tags)
	for _, tag := range tags {
		group := groups[tag]
		first := group[0]
		on := slices.Clone(first.On)
		slices.Sort(on)
		named := false
		for _, r := range group {
			other := slices.Clone(r.On)
			slices.Sort(other)
			if !slices.Equal(on, other) {
				return fmt.Errorf("session %s: recipes %s and %s run on different targets", tag, first.Name, r.Name)
			}
			if credentialsOf(r) != credentialsOf(first) {
				return fmt.Errorf("session %s: recipes %s and %s hold different credentials", tag, first.Name, r.Name)
			}
			named = named || r.Name == tag
		}
		if !named {
			return fmt.Errorf("session %s: no recipe is named %s, the group's default", tag, tag)
		}
	}
	return nil
}

func credentialsOf(r *Recipe) string {
	if r.Credentials == "" {
		return CredentialsFull
	}
	return r.Credentials
}

// Builtin returns the recipe that ships with factory under name, as bytes
// to hand to the sandbox and parsed to fail early here.
func Builtin(name string) ([]byte, *Recipe, error) {
	data, err := builtinFS.ReadFile("recipes/" + name + ".yaml")
	if err != nil {
		return nil, nil, fmt.Errorf("no built-in recipe %q: %w", name, err)
	}
	r, err := Parse(data)
	if err != nil {
		return nil, nil, fmt.Errorf("built-in recipe %s: %w", name, err)
	}
	return data, r, nil
}
