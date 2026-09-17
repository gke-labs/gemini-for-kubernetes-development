package watch

import (
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
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/issues"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/prs"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	githubv39 "github.com/google/go-github/v39/github"
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
	PollInterval        time.Duration
	Assignee            string
	AssigneeChanged     bool
	Labels              []string
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
}

type Watcher struct {
	common.RootFlags
	Flags

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
	sandboxes        *sandbox.Service
	dispatcher       *dispatcher.Dispatcher
	reconciler       *sandbox.Reconciler
	chores           *chores.Scheduler
	issueScanner     *issues.Scanner
	prScanner        *prs.Scanner
	timeoutChan      <-chan time.Time
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
	w.reconciler = w.newReconciler()
	w.chores = w.newChoreScheduler()
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
