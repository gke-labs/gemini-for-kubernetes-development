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
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/issues"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/prs"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/scan"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/warmworkspace"
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
	// Preconditions drops a queued task before it starts when a fact it
	// was decided on no longer holds (api.QueueTask.Expect).
	Preconditions bool
	// Attempts counts a give-up's attempts from the queue's journal of runs
	// rather than from the watcher's comments (prs.Config.Attempts).
	Attempts bool
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
	// RulesEngine is how the watch rules run beside the scanners: "" or
	// "off", not at all; "shadow", deciding on every decision the scanners
	// make and counting where they differ, served on the queue API; "on",
	// shadow plus the rules RulesUse hands to the engine deciding for the
	// scanners.
	RulesEngine string
	// RulesUse hands rules to the engine with RulesEngine on:
	// "conflict=engine,ci=engine" (legacy, the default, is the scanners').
	RulesUse string
	// RulesCanaryLabel, when set, has the engine decide the rules it was
	// handed only on items carrying this label; the rest stay the
	// scanners'.
	RulesCanaryLabel string
	// RulesCanaryPercent has the PR scanner put RulesCanaryLabel on that
	// share of the pull requests it works on, chosen by number
	// (rules.Control.Canary).
	RulesCanaryPercent int
	// RulesAtStart has the engine decide again at start, for the rules it
	// decides, and drop a task it no longer decides
	// (dispatcher.Config.AtStart, watcherTaskCoordinator.Redecide).
	RulesAtStart bool
	// RulesDisabled are rules the watch does not act on at all, through
	// the scanners or the engine: the operator's emergency switch
	// ("review,ci").
	RulesDisabled string
	// RulesRecord is the file the issue and PR scanners record each
	// decision in, with the facts it was made on, for the watch rules'
	// replay (`factory watch rules replay`); empty records nothing. What
	// they decide is unchanged.
	RulesRecord string
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

	cfg          *config.FactoryConfig
	triggerLabel string
	ghClient     *githubv39.Client
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
	// set, and hands them to rulesShadow when the rules run in shadow.
	rulesRecorder *rules.Recorder
	rulesShadow   *rules.Shadow
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
	warmWorkspace *warmworkspace.Controller
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
	w.warmWorkspace = w.newWarmWorkspaceController()
	w.reconciler = w.newReconciler()
	w.chores = w.newChoreScheduler()
	w.sharedScan = w.newSharedScan()
	w.rulesControl = w.newRulesControl()
	w.rulesShadow = w.newRulesShadow()
	w.rulesRecorder = rules.NewRecorder(w.RulesRecord, rules.DefaultRecordBytes, w.rulesShadow)
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

// newRulesControl is the operator's say over the rules, from the flags.
// Settings that do not check out (an engine it does not know, a rule that
// is not one) leave the rules off with a warning rather than stop the
// watch: the scanners alone, every rule on.
func (w *Watcher) newRulesControl() *rules.Control {
	if w.RulesEngine == "" && w.RulesUse == "" && w.RulesCanaryLabel == "" && w.RulesCanaryPercent == 0 && w.RulesDisabled == "" {
		return nil
	}
	set, err := rules.Builtin()
	if err != nil {
		klog.Warningf("Loading the watch rules: %v; running without them", err)
		return nil
	}
	use, err := rules.ParseUse(w.RulesUse)
	if err == nil {
		var c *rules.Control
		if c, err = rules.NewControl(w.RulesEngine, use, w.RulesCanaryLabel, rules.ParseList(w.RulesDisabled), set); err == nil {
			if err = c.SetCanaryPercent(w.RulesCanaryPercent); err != nil {
				klog.Warningf("Watch rules settings: %v; running without the rules", err)
				return nil
			}
			klog.Infof("Watch %s", c)
			return c
		}
	}
	klog.Warningf("Watch rules settings: %v; running without the rules", err)
	return nil
}

// newRulesShadow is the rules in shadow, when the engine is shadow or on:
// the rules the engine decides with, compared with every decision the
// scanners make.
func (w *Watcher) newRulesShadow() *rules.Shadow {
	if !w.rulesControl.Shadows() {
		return nil
	}
	klog.Infof("Watch rules in shadow: %d rules decide beside the scanners", len(w.rulesControl.Set()))
	return rules.NewShadow(w.rulesControl.Set())
}
