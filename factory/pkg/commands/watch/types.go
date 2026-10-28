package watch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/chores"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/concurrency"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/dispatcher"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/fanouts"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/graphs"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/issues"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/prs"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/retros"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/scan"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/warmworkspace"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/whyclosed"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"
)

type RepoFlag struct {
	Owner string
	Repo  string
}

func (r *RepoFlag) String() string {
	if r == nil || (r.Owner == "" && r.Repo == "") {
		return ""
	}
	return fmt.Sprintf("%s/%s", r.Owner, r.Repo)
}

func (r *RepoFlag) Set(val string) error {
	parts := strings.Split(val, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("invalid repo format, expected owner/repo, got %s", val)
	}
	r.Owner = parts[0]
	r.Repo = parts[1]
	return nil
}

func (r *RepoFlag) Type() string {
	return "string"
}

type Flags struct {
	Repo                RepoFlag
	Assignee            string
	AssigneeChanged     bool
	DryRun              bool
	WatchTimeout        time.Duration
	MaxActions          int
	MaxPending          int
	Mode                string
	QueueDir            string
	Once                bool
	IssueMode           string
	PRMode              string
	ChoresMode          string
	ScanLimit           int
	TaskTimeout         time.Duration
	SandboxEvictionAge  string
	SandboxIdleTimeout  time.Duration
	PRInactivityTimeout time.Duration
	// SharedScan makes the issue and pull request scanners read one shared,
	// incremental set of listings, persisted in the queue directory
	// (package scan), instead of each listing for itself.
	SharedScan bool
	// ScanState is the file the shared scan's snapshot persists in. Empty
	// keeps it in the queue directory (scan_state.json); the overseer puts
	// it on its volume outside the queue directory, which run.sh pushes to
	// a branch of the repository on every cycle.
	ScanState string
	// GraphQLPRDetails makes the PR scanner read the details of the pull
	// requests it evaluates in GraphQL batches (prs.Config.GraphQLPRDetails).
	GraphQLPRDetails bool
	// Graphs runs the issue graph subcontroller (package graphs): parents
	// labelled '<trigger>/graph', and planned issues whose graph started.
	Graphs bool
	// RulesDisabled are rules the watch does not act on at all, through
	// the scanners or the engine: the operator's emergency switch
	// ("review,ci").
	RulesDisabled string
	// RulesRecord is the file the issue and PR scanners record each
	// decision in, with the facts it was made on, for the watch rules'
	// replay (`factory watch rules replay`); empty records nothing. What
	// they decide is unchanged.
	RulesRecord string
	// RetroSchedule is when the weekly retro runs (package retros): cron's
	// five fields or a descriptor (@weekly). Empty, it never does.
	RetroSchedule string
}

type Watcher struct {
	common.RootFlags
	Flags

	// ProposeFanout runs the fanout recipe on an issue and applies its
	// result (pkg/commands' runRecipe, which this package cannot import),
	// as user, or the watcher's own account if that is empty. Nil, fan-out
	// parents without a spec wait for a person to write one.
	ProposeFanout func(ctx context.Context, issueURL, user string) error
	// WarmWorkspace moves the repository's warm workspace cycle on by a
	// step (pkg/commands' warmWorkspace), cloning as user. Nil, or no
	// warmWorkspace in the config, the repository is not warmed.
	WarmWorkspace func(ctx context.Context, repoURL, user string, cfg config.WarmWorkspaceConfig) error
	// Retro runs the retro of the repository for the window since, as
	// user, and opens its Change as a PR (pkg/commands' watchRetro, what
	// factory retro runs). Nil, or no RetroSchedule, no retro runs.
	Retro func(ctx context.Context, repoURL, user string, since time.Time) error
	// RepoRules lays the repository's own watch rules (.agents/watch) over
	// factory's (base), as the repository recipe mode says, and says where
	// they came from ("" for none) - pkg/commands' repoWatchRules. Nil,
	// factory's rules alone. Read once, when the watch starts.
	RepoRules func(ctx context.Context, base rules.Set) (rules.Set, string, error)
	// RepoRecipes is what the watch reads of the repository's own recipes
	// (.agents/recipes), by name, as the repository recipe mode lets them
	// in (pkg/commands' repoRecipes). Nil, factory's recipes alone. Read
	// once, when the watch starts.
	RepoRecipes func(ctx context.Context) (map[string]RecipeFacts, error)

	repoRecipes  map[string]RecipeFacts
	cfg          *config.FactoryConfig
	triggerLabel string
	// prListingLabels are the labels the PR listings add for the rules the
	// pool and trigger listings do not reach (rules.Set.ListingLabels).
	prListingLabels []string
	// issueListingLabels are the same for the rules on issues.
	issueListingLabels []string
	ghClient           *githubv39.Client
	// repoClient is the repository-bound view of ghClient, built once and
	// shared so that subcontrollers take the owner and repo with the client
	// rather than as three separate pieces of configuration.
	repoClient       *github.Client
	kubeClient       *clients.KubernetesClient
	githubLogin      string
	targetAssignee   string
	allBotUsers      []string
	incomingDir      string
	processingDir    string
	processedDir     string
	processingLogDir string
	processedLogDir  string
	queueMgr         *concurrency.TaskQueueManager
	sandboxLocks     *concurrency.SandboxLockRegistry
	entityCache      *concurrency.EntityStateCache
	// sharedScan is the listings both scanners read when SharedScan is on.
	sharedScan *scan.Store
	// rulesRecorder records the scanners' decisions when RulesRecord is
	// set, and hands each to rulesSummary.
	rulesRecorder *rules.Recorder
	rulesSummary  *rules.Summary
	// rulesControl is the operator's say over the rules (RulesEngine,
	// RulesUse, RulesCanaryLabel, RulesDisabled); nil is the scanners
	// alone, every rule on.
	rulesControl  *rules.Control
	sandboxes     *sandbox.Service
	dispatcher    *dispatcher.Dispatcher
	reconciler    *sandbox.Reconciler
	chores        *chores.Scheduler
	issueScanner  *issues.Scanner
	prScanner     *prs.Scanner
	fanouts       *fanouts.Controller
	graphs        *graphs.Controller
	warmWorkspace *warmworkspace.Controller
	retro         *retros.Controller
	whyClosed     *whyclosed.Tracker
	timeoutChan   <-chan time.Time
}

// initComponents builds the in-memory primitives and subcontrollers of the
// watcher. It is called once from NewWatcher so that tests can drive individual
// subcontrollers, and again from init once the GitHub and Kubernetes clients
// have been resolved.
func (w *Watcher) initComponents() {
	if w.incomingDir == "" && w.QueueDir != "" {
		w.incomingDir = filepath.Join(w.QueueDir, "incoming")
		w.processingDir = filepath.Join(w.QueueDir, "processing")
		w.processedDir = filepath.Join(w.QueueDir, "processed")
		logDir := os.Getenv("FACTORY_LOGS")
		if logDir == "" {
			logDir = filepath.Join(w.QueueDir, "logs")
		}
		w.processingLogDir = filepath.Join(logDir, "processing")
		w.processedLogDir = filepath.Join(logDir, "processed")
	}
	w.queueMgr = concurrency.NewTaskQueueManager(concurrency.TaskQueueManagerConfig{
		QueueDir:         w.QueueDir,
		IncomingDir:      w.incomingDir,
		ProcessingDir:    w.processingDir,
		ProcessedDir:     w.processedDir,
		ProcessingLogDir: w.processingLogDir,
		ProcessedLogDir:  w.processedLogDir,
		DryRun:           w.DryRun,
	})
	w.repoClient = github.ForRepo(w.ghClient, w.Repo.Owner, w.Repo.Repo)
	w.sandboxLocks = concurrency.NewSandboxLockRegistry()
	w.entityCache = concurrency.NewEntityStateCache()
	w.sandboxes = sandbox.NewService(sandbox.ServiceConfig{
		Namespace: w.Namespace,
		Owner:     w.Repo.Owner,
		Repo:      w.Repo.Repo,
	}, sandbox.ServiceDeps{
		Kube:   w.kubeClient,
		GitHub: w.ghClient,
	})
	w.dispatcher = w.newDispatcher(w.newCLIRunner())
	w.fanouts = w.newFanoutController()
	w.graphs = w.newGraphController()
	w.warmWorkspace = w.newWarmWorkspaceController()
	w.retro = w.newRetroController()
	w.whyClosed = w.newWhyClosed()
	w.reconciler = w.newReconciler()
	w.chores = w.newChoreScheduler()
	w.repoRecipes = w.loadRepoRecipes()
	w.rulesControl = w.newRulesControl()
	w.prListingLabels = w.ruleListingLabels(rules.OnPR)
	w.issueListingLabels = w.ruleListingLabels(rules.OnIssue)
	// After the rules: what it tracks includes the labels they are listed by.
	w.sharedScan = w.newSharedScan()
	w.rulesSummary = w.newRulesSummary()
	w.rulesRecorder = rules.NewRecorder(w.RulesRecord, rules.DefaultRecordBytes, w.rulesSummary)
	w.issueScanner = w.newIssueScanner()
	w.prScanner = w.newPRScanner()
}

// Wait blocks until all in-flight tasks have completed.
func (w *Watcher) Wait() {
	if w.dispatcher != nil {
		w.dispatcher.Wait()
	}
}

func NewWatcher(rootFlags common.RootFlags, flags Flags) *Watcher {
	w := &Watcher{
		RootFlags: rootFlags,
		Flags:     flags,
	}
	w.initComponents()
	return w
}

// newRulesControl is the operator's say over the rules, from the flags:
// the engine decides every rule, but those disabled. A disabled rule that
// is not one leaves the watch with every rule on, and a warning, rather
// than stopping it; prebaked rules that fail to load leave the scanners
// alone.
func (w *Watcher) newRulesControl() *rules.Control {
	set, err := rules.Builtin()
	if err != nil {
		klog.Warningf("Loading the watch rules: %v; running without them", err)
		return nil
	}
	if w.RepoRules != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		repoSet, from, err := w.RepoRules(ctx, set)
		cancel()
		switch {
		case err != nil:
			klog.Warningf("Loading the repository's watch rules: %v; running factory's", err)
		case from != "":
			set = repoSet
			klog.Infof("Watch rules: the repository's %s", from)
		}
	}
	c, err := rules.NewControl(rules.ParseList(w.RulesDisabled), set)
	if err != nil {
		klog.Warningf("Watch rules settings: %v; every rule on", err)
		if c, err = rules.NewControl(nil, set); err != nil {
			return nil
		}
	}
	klog.Infof("Watch %s", c)
	return c
}

// RecipeFacts are what the watch reads of a recipe: who it runs as, and
// the key of the one comment its result is written as (its
// task-output.comment.key; "" for none).
type RecipeFacts struct {
	As         string
	CommentKey string
}

// loadRepoRecipes reads the repository's recipes (RepoRecipes); none,
// with a warning, when that fails: their tasks then run as their type's
// role.
func (w *Watcher) loadRepoRecipes() map[string]RecipeFacts {
	if w.RepoRecipes == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	facts, err := w.RepoRecipes(ctx)
	if err != nil {
		klog.Warningf("Reading the repository's recipes: %v", err)
		return nil
	}
	return facts
}

// newRulesSummary tallies what the rules decide (rules.Summary), served on
// the queue API; nil without a control.
func (w *Watcher) newRulesSummary() *rules.Summary {
	if w.rulesControl == nil {
		return nil
	}
	return rules.NewSummary(w.rulesControl.Set())
}
