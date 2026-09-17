package prs

import (
	"os"
	"path/filepath"
	"testing"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/concurrency"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
)

func stringPtr(s string) *string { return &s }

// newTestKubeClient returns a client backed by an empty fake cluster, which
// makes every sandbox lookup report "not running" - the state in which the
// scanner is free to queue work.
func newTestKubeClient() *clients.KubernetesClient {
	scheme := runtime.NewScheme()
	fakeDynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		k8s.SandboxGVR: "SandboxList",
	})
	return &clients.KubernetesClient{
		DynamicClient: fakeDynamic,
	}
}

// testOpts are the parts of a Scanner's configuration a test cares about. The
// rest are fixed by newTestScanner so that individual tests do not restate them.
type testOpts struct {
	// GitHub is the client, normally pointed at an httptest server.
	GitHub *githubv39.Client
	// Kube backs the sandbox service. A nil client makes every sandbox lookup
	// report "not running", which is what most tests want.
	Kube *clients.KubernetesClient
	// BotUsers is the pool of accounts whose pull requests are evaluated.
	BotUsers []string
	// GitHubLogin is the watcher's own account.
	GitHubLogin string
	// TriggerLabel is the label prefix under test.
	TriggerLabel string
	// ReviewerLogins are the accounts whose reviews count as review feedback.
	ReviewerLogins []string
	// AllowlistedBots are the automated accounts whose comments are acted on.
	AllowlistedBots []string
	// MinNumber skips pull requests numbered below it.
	MinNumber int
}

// newTestScanner builds a Scanner over a real queue manager rooted at tempDir,
// and returns both so that a test can assert on the queue as well as on GitHub.
//
// The worker pool is pinned to one so that a cycle evaluates its candidates in
// the order they were given: several tests assert on the sequence of calls the
// httptest server saw, which a concurrent pool would interleave.
func newTestScanner(t *testing.T, tempDir string, opts testOpts) (*Scanner, *concurrency.TaskQueueManager) {
	t.Helper()

	incomingDir := filepath.Join(tempDir, "incoming")
	processingDir := filepath.Join(tempDir, "processing")
	processedDir := filepath.Join(tempDir, "processed")
	for _, dir := range []string{incomingDir, processingDir, processedDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("creating %s: %v", dir, err)
		}
	}

	queue := concurrency.NewTaskQueueManager(concurrency.TaskQueueManagerConfig{
		QueueDir:      tempDir,
		IncomingDir:   incomingDir,
		ProcessingDir: processingDir,
		ProcessedDir:  processedDir,
	})

	sandboxes := sandbox.NewService(sandbox.ServiceConfig{
		Namespace: "test-ns",
		Owner:     "test-owner",
		Repo:      "test-repo",
	}, sandbox.ServiceDeps{
		Kube:   opts.Kube,
		GitHub: opts.GitHub,
	})

	scanner := New(Config{
		Workers:         1,
		TriggerLabel:    opts.TriggerLabel,
		GitHubLogin:     opts.GitHubLogin,
		BotUsers:        opts.BotUsers,
		ReviewerLogins:  opts.ReviewerLogins,
		AllowlistedBots: opts.AllowlistedBots,
		MinNumber:       opts.MinNumber,
		ProcessedDir:    processedDir,
	}, Deps{
		GitHub:    github.ForRepo(opts.GitHub, "test-owner", "test-repo"),
		Queue:     queue,
		Entities:  concurrency.NewEntityStateCache(),
		Sandboxes: sandboxes,
	})

	return scanner, queue
}
