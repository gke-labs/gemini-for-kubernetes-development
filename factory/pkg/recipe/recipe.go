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
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskoutput"
)

// Recipe is one task.
type Recipe struct {
	Name string `yaml:"name"`
	// Context is the standing rules: what the agent may and may not do.
	// It is sent once, ahead of the first ask, rather than repeated in
	// every turn.
	Context string `yaml:"context,omitempty"`
	// Inputs declares what the recipe takes beyond what its caller always
	// sets, so a missing or misspelt one fails before a sandbox is made.
	Inputs map[string]Input `yaml:"inputs,omitempty"`
	Steps  []Step           `yaml:"steps"`
	// Outputs are the task-directory files `recipe run` prints when the
	// recipe ends, in order. Unset, they are the capture files.
	Outputs []string `yaml:"outputs,omitempty"`
	// TaskOutput declares the task's result: the runner wraps the file the
	// agent wrote into taskoutput.File, which `factory apply` acts on. It
	// is factory's, not the runner's: ForSandbox takes it out of the
	// recipe a sandbox gets, and the task carries it in task.json.
	TaskOutput *taskoutput.Decl `yaml:"task-output,omitempty"`
}

// Input is one declared input.
type Input struct {
	Description string `yaml:"description,omitempty"`
	Default     string `yaml:"default,omitempty"`
	Required    bool   `yaml:"required,omitempty"`
	// Type is "" for a string, or InstructionsType. Like task-output it is
	// factory's: ForSandbox drops it.
	Type string `yaml:"type,omitempty"`
}

// InstructionsType is an input given any number of times on the command
// line, each a local file, a file in the repository or the text itself,
// as `factory pr review --instruction` takes them; the recipe gets them
// joined into one string.
const InstructionsType = "instructions"

// Step is one step; exactly one of Uses, Run and Ask is set.
type Step struct {
	// ID names the step for later ones: {{ .Steps.<id>.ExitCode }}.
	ID   string            `yaml:"id,omitempty"`
	Uses string            `yaml:"uses,omitempty"`
	With map[string]string `yaml:"with,omitempty"`
	Run  string            `yaml:"run,omitempty"`
	Ask  string            `yaml:"ask,omitempty"`
	// Capture writes an ask's reply to this file in the task directory,
	// where the CLI reads it back, as runEngine's output file is today.
	Capture string `yaml:"capture,omitempty"`
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
	"checkout-default-branch": "checkoutDefaultBranch",
	"checkout-pr-branch":      "checkoutPRBranch",
	"configure-engine":        "configureGemini",
}

var (
	stepIDRE  = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	inputRE   = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	withKeyRE = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
)

// Parse reads and validates a recipe.
func Parse(data []byte) (*Recipe, error) {
	var r Recipe
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("parsing recipe: %w", err)
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return &r, nil
}

// Validate checks what can be checked before anything runs, so a bad
// recipe fails in the CLI and not halfway through a sandbox.
func (r *Recipe) Validate() error {
	if r.Name == "" {
		return fmt.Errorf("recipe has no name")
	}
	if len(r.Steps) == 0 {
		return fmt.Errorf("recipe %s has no steps", r.Name)
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
	}
	if to := r.TaskOutput; to != nil {
		if !taskoutput.Known(to.Kind) {
			return fmt.Errorf("task-output kind %q is not one of: %s", to.Kind, strings.Join(taskoutput.KnownKinds(), ", "))
		}
		if !safeFileName(to.From) {
			return fmt.Errorf("task-output from %q must be a plain file name", to.From)
		}
	}
	for _, o := range r.Outputs {
		if !safeFileName(o) {
			return fmt.Errorf("output %q must be a plain file name", o)
		}
	}
	ids := map[string]bool{}
	for i, s := range r.Steps {
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
		if s.ContinueOnError && s.Run == "" {
			return fmt.Errorf("step %d: continue-on-error is only for run steps", i+1)
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

// OutputFiles is what `recipe run` prints: Outputs, or else every file an
// ask captures, in step order.
func (r *Recipe) OutputFiles() []string {
	if len(r.Outputs) > 0 {
		return r.Outputs
	}
	var out []string
	for _, s := range r.Steps {
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

// ForSandbox is the recipe as a sandbox's runner gets it: without
// task-output and input types, which runners older than them reject as
// unknown fields.
func ForSandbox(data []byte) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing recipe: %w", err)
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return data, nil
	}
	m := doc.Content[0]
	changed := dropKey(m, "task-output")
	if inputs := mapValue(m, "inputs"); inputs != nil && inputs.Kind == yaml.MappingNode {
		for i := 1; i < len(inputs.Content); i += 2 {
			if inputs.Content[i].Kind == yaml.MappingNode && dropKey(inputs.Content[i], "type") {
				changed = true
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
