package watch

import (
	"context"
	"sync"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	githubv39 "github.com/google/go-github/v39/github"
)

type QueueTask struct {
	Type        string    `yaml:"type"` // "issue-fix", "pr-investigate", "pr-comments", "pr-iterate", "pr-review", "agent-chore"
	URL         string    `yaml:"url"`
	Number      int       `yaml:"number"`
	Priority    string    `yaml:"priority"` // "critical", "urgent", "important", "high", "medium", "low"
	Phase       int       `yaml:"phase"`    // 1: Rebase/iterate, 2: Comments, 3: Investigate/Fix, 4: Chores
	CreatedAt   time.Time `yaml:"createdAt"`
	Assignee    string    `yaml:"assignee,omitempty"`
	Status      string    `yaml:"status"` // "Pending", "Running", "Completed", "Failed"
	Error       string    `yaml:"error,omitempty"`
	AgentFile   string    `yaml:"agentFile,omitempty"` // For chore tasks
	SessionID   string    `yaml:"sessionId,omitempty"` // For workflow sessions
	CommitSHA   string    `yaml:"commitSHA,omitempty"`
	Recovered   bool      `yaml:"recovered,omitempty"`
	CompletedAt time.Time `yaml:"completedAt,omitempty"`
}

type ChoreRunState struct {
	LastRun time.Time `json:"lastRun"`
}

type Options struct {
	Owner            string
	Repo             string
	Interval         time.Duration
	Assignee         string
	AssigneeChanged  bool
	Labels           []string
	DryRun           bool
	WatchTimeout     time.Duration
	MaxActions       int
	MaxPending       int
	Mode             string
	QueueDir         string
	Once             bool
	IssueMode        string
	PRMode           string
	ChoresMode       string
	EphemeralStorage string
	Secrets          []factorysandbox.SecretMount
	ScanLimit        int
	TaskTimeout      time.Duration
	Namespace        string
	SecretName       string
	Image            string
	DiskSize         string
}

type prWatchState struct {
	lastSHA                  string
	lastInvestigatedTime     time.Time
	lastCommentAddressedTime time.Time
}

type watchState struct {
	mu               sync.Mutex
	openPRs          []*githubv39.PullRequest
	referencedIssues map[int]bool
	lastPRScan       time.Time
	lastIssueScan    time.Time
	lastRunnerRun    time.Time
	shuttingDown     bool
}

type watchContext struct {
	ctx             context.Context
	opts            Options
	ghClient        *githubv39.Client
	kubeClient      *clients.KubernetesClient
	cfg             *config.FactoryConfig
	githubLogin     string
	targetAssignee  string
	allBotUsers     []string
	incomingDir     string
	processingDir   string
	processedDir    string
	queueDir        string
	processedIssues map[int]time.Time
	processedPRs    map[int]prWatchState
	state           *watchState
	triggerLabel    string
}
