package watch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/concurrency"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/dispatcher"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
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

	cfg              *config.FactoryConfig
	triggerLabel     string
	ghClient         *githubv39.Client
	kubeClient       *clients.KubernetesClient
	githubLogin      string
	targetAssignee   string
	allBotUsers      []string
	incomingDir      string
	processingDir    string
	processedDir     string
	processingLogDir string
	processedLogDir  string
	processedIssues  map[int]time.Time
	processedPRs     map[int]prWatchState
	queueMgr         *concurrency.TaskQueueManager
	sandboxLocks     *concurrency.SandboxLockRegistry
	entityCache      *concurrency.EntityStateCache
	sandboxes        *sandbox.Service
	dispatcher       *dispatcher.Dispatcher
	reconciler       *sandbox.Reconciler
	state            *watchState
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
	w.sandboxLocks = concurrency.NewSandboxLockRegistry()
	w.entityCache = concurrency.NewEntityStateCache()
	// Allocated before the subcontrollers below, because the reconciler's pause
	// signal reads the shutdown flag out of it.
	w.state = &watchState{}
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

type ChoreRunState struct {
	LastRun time.Time `json:"lastRun"`
}

// prWatchState tracks the progress and state of automated tasks for a monitored pull request.
type prWatchState struct {
	// lastInvestigatedTime is the timestamp when a CI failure investigation was last queued or completed.
	lastInvestigatedTime time.Time
	// lastInvestigatedSHA is the head commit SHA when CI failures were last investigated.
	lastInvestigatedSHA string
	// lastCommentAddressedTime is the timestamp when PR comments were last addressed by the bot.
	lastCommentAddressedTime time.Time
	// lastCommentAddressedSHA is the head commit SHA when review comments were last addressed, preventing duplicate comment processing on the same commit.
	lastCommentAddressedSHA string
	// lastReviewedSHA is the commit SHA for which an automated PR review was last queued or completed.
	lastReviewedSHA string
	// lastIteratedSHA is the commit SHA for which a rebase/conflict-resolution task was last queued or completed.
	lastIteratedSHA string
	// lastIteratedTime is the timestamp when a rebase/conflict-resolution task was last queued or completed.
	lastIteratedTime time.Time
}

// watchState tracks the cadence of the scan cycles and the shutdown signal.
// Open pull request and issue state lives in the shared EntityStateCache.
type watchState struct {
	mu            sync.Mutex
	lastPRScan    time.Time
	lastIssueScan time.Time
	shuttingDown  bool
}
