package common

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	"gopkg.in/yaml.v3"
	"k8s.io/klog/v2"
)

type AgentDefinition struct {
	Name               string            `yaml:"name"`
	Description        string            `yaml:"description"`
	Schedule           string            `yaml:"schedule"`
	SkipPR             bool              `yaml:"skipPR,omitempty"`
	Mode               string            `yaml:"mode,omitempty"`
	Cooldown           string            `yaml:"cooldown,omitempty"`
	PreconditionScript string            `yaml:"preconditionScript,omitempty"`
	Redirect           string            `yaml:"redirect,omitempty"`
	Recipe             string            `yaml:"recipe,omitempty"`
	Inputs             map[string]string `yaml:"inputs,omitempty"`
	Prompt             string            `yaml:"-"`
}

func ParseAgent(content []byte) (*AgentDefinition, error) {
	parts := strings.SplitN(string(content), "---", 3)
	if len(parts) < 3 {
		return nil, fmt.Errorf("invalid agent definition format: missing frontmatter")
	}

	var def AgentDefinition
	if err := yaml.Unmarshal([]byte(parts[1]), &def); err != nil {
		return nil, fmt.Errorf("failed to unmarshal frontmatter: %w", err)
	}

	def.Prompt = strings.TrimSpace(parts[2])
	return &def, nil
}

// AgentTaskID is the task id an agent's run is known by, which names its
// sandbox (sandbox.AgentSandboxName): its definition's name, slugified,
// and its session, if it is a workflow's (issue-<n>).
func AgentTaskID(name, sessionID string) string {
	if sessionID != "" {
		return Slugify(name) + "-" + sessionID
	}
	return Slugify(name)
}

func Slugify(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, " ", "-")
	var res strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			res.WriteRune(r)
		}
	}
	return res.String()
}

func ParseGitHubURL(urlStr string) (owner, repo, branch, path string, ok bool) {
	u, err := url.Parse(urlStr)
	if err != nil {
		return "", "", "", "", false
	}
	if u.Host != "github.com" {
		return "", "", "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) < 4 || (parts[2] != "blob" && parts[2] != "raw") {
		return "", "", "", "", false
	}
	owner = parts[0]
	repo = parts[1]
	branch = parts[3]
	path = strings.Join(parts[4:], "/")
	return owner, repo, branch, path, true
}

func SanitizeWorkflowPath(path string) string {
	path = strings.TrimSpace(path)
	for strings.HasSuffix(path, `\n`) || strings.HasSuffix(path, `\r`) {
		path = strings.TrimSuffix(strings.TrimSuffix(path, `\n`), `\r`)
		path = strings.TrimSpace(path)
	}
	return path
}

var (
	workflowURLRegex  = regexp.MustCompile(`(?:\s|^)(https?://[^\s\)"'` + "`" + `]+(?:\.(?:md|txt|yaml)|/(?:workflows|agents)/)[^\s\)"'` + "`" + `]*)`)
	workflowFileRegex = regexp.MustCompile(`(?:\s|^)(\.?\.?/?(?:\.?agents?|\.gemini)/[a-zA-Z0-9_\-\./]+)\b`)
)

func FindWorkflowPath(body string) string {
	urlMatch := workflowURLRegex.FindStringSubmatch(body)
	if len(urlMatch) > 1 {
		return SanitizeWorkflowPath(urlMatch[1])
	}

	matches := workflowFileRegex.FindStringSubmatch(body)
	if len(matches) > 1 {
		return SanitizeWorkflowPath(matches[1])
	}
	return ""
}

func FetchWorkflowContent(ctx context.Context, ghClient *github.Client, urlStr string) ([]byte, error) {
	content, err := fetchWorkflowContentOnce(ctx, ghClient, urlStr)
	if err != nil {
		return nil, err
	}
	return ResolveWorkflowRedirect(ctx, ghClient, content)
}

func ResolveWorkflowRedirect(ctx context.Context, ghClient *github.Client, content []byte) ([]byte, error) {
	def, err := ParseAgent(content)
	if err != nil || def.Redirect == "" {
		return content, nil
	}
	target := SanitizeWorkflowPath(def.Redirect)
	klog.Infof("Following workflow redirect to %s", target)
	nextContent, err := fetchWorkflowContentOnce(ctx, ghClient, target)
	if err != nil {
		return nil, fmt.Errorf("following workflow redirect to %s: %w", target, err)
	}
	if nextDef, err := ParseAgent(nextContent); err == nil && nextDef.Redirect != "" {
		return nil, fmt.Errorf("%w: chained workflow redirects are not supported (redirected from %s to %s)", ErrWorkflowUnusable, target, nextDef.Redirect)
	}
	return nextContent, nil
}

// workflowFetchTimeout bounds each request for a workflow definition. The URL
// comes from an issue body, so a server that never answers must not be able
// to stall the caller - in the watch daemon, a whole scanner goroutine.
var workflowFetchTimeout = 30 * time.Second

func fetchWorkflowContentOnce(ctx context.Context, ghClient *github.Client, urlStr string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, workflowFetchTimeout)
	defer cancel()

	urlStr = SanitizeWorkflowPath(urlStr)
	if ghClient != nil {
		if owner, repo, branch, path, ok := ParseGitHubURL(urlStr); ok {
			klog.Infof("Fetching agent from GitHub repository %s/%s at branch/ref %s, path %s", owner, repo, branch, path)
			// The URL may point outside the repository this client is bound to,
			// which is the whole reason a workflow can be referenced by URL.
			contentStr, err := ghClient.FileContentIn(ctx, owner, repo, path, branch)
			if err != nil {
				if github.IsNotFound(err) {
					return nil, fmt.Errorf("fetching content from GitHub repo: %w: %w", ErrWorkflowNotFound, err)
				}
				return nil, fmt.Errorf("fetching content from GitHub repo: %w", err)
			}
			return []byte(contentStr), nil
		}
	}

	klog.Infof("Fetching agent from HTTP URL %s", urlStr)
	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP GET request failed: %w", err)
	}
	defer resp.Body.Close()

	if err := classifyHTTPStatus(urlStr, resp.StatusCode); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// classifyHTTPStatus turns a non-OK response for a workflow URL into an error
// that says whether retrying could help. Only a timeout, a rate limit or a
// server error can clear up by itself; any other client error will not
// change until someone fixes the URL or the server it points at.
func classifyHTTPStatus(urlStr string, status int) error {
	switch {
	case status == http.StatusOK:
		return nil
	case status == http.StatusNotFound || status == http.StatusGone:
		return fmt.Errorf("%w: HTTP GET %s returned status %d", ErrWorkflowNotFound, urlStr, status)
	case status >= 400 && status < 500 && status != http.StatusRequestTimeout && status != http.StatusTooManyRequests:
		return fmt.Errorf("%w: HTTP GET %s returned status %d", ErrWorkflowUnusable, urlStr, status)
	default:
		return fmt.Errorf("HTTP GET returned status %d", status)
	}
}

// ErrWorkflowNotFound reports that a workflow definition, or the target of its
// redirect, does not exist (or is not visible to the caller), as opposed to a
// failure to fetch it that may be transient.
var ErrWorkflowNotFound = errors.New("workflow definition not found")

// ErrWorkflowUnusable reports that a workflow definition cannot be loaded for
// a reason that will not go away by itself - a chained redirect, or a URL the
// server refuses - as opposed to a failure that may be transient.
var ErrWorkflowUnusable = errors.New("workflow definition cannot be used")

// WorkflowStatus is the outcome of checking a path an issue names as its
// workflow.
type WorkflowStatus int

const (
	// NotAWorkflow means the path names something other than a workflow
	// definition: an ordinary skill or agent prompt, or a file that could not
	// be read and does not follow the workflow naming convention.
	NotAWorkflow WorkflowStatus = iota
	// WorkflowFound means the path names a workflow definition that exists.
	WorkflowFound
	// WorkflowMissing means the path follows the workflow naming convention
	// but the definition, or the target of its redirect, does not exist.
	WorkflowMissing
	// WorkflowUnresolved means the path follows the workflow naming convention
	// but its definition could not be fetched for a reason that may be
	// transient, so whether it exists is not known.
	WorkflowUnresolved
	// WorkflowUnusable means the path follows the workflow naming convention
	// but its definition cannot be loaded for a reason that will not go away
	// by itself, such as a chained redirect.
	WorkflowUnusable
)

// IsWorkflowPathByConvention reports whether a path is a workflow by naming
// convention alone: it sits under a workflows/ directory or, for a URL, a
// workflows/ or agents/ directory. Whether any other path is a workflow
// depends on its content.
func IsWorkflowPathByConvention(path string) bool {
	isURL := strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://")
	return strings.Contains(path, "/workflows/") || (isURL && strings.Contains(path, "/agents/"))
}

// CheckWorkflowDefinition reports whether path names a workflow definition
// and, for a path that is a workflow by naming convention (see
// IsWorkflowPathByConvention), whether that definition can be loaded. Such a
// definition is fetched the way `agent create` will fetch it, redirect
// included, so WorkflowFound means the run can at least load it.
//
// Any other path is only a workflow if its content declares workflow mode, so
// one that cannot be read is NotAWorkflow: an issue that merely mentions such
// a path is not asking for a workflow. The returned error, if any, explains
// why the definition could not be fetched.
func CheckWorkflowDefinition(ctx context.Context, ghClient *github.Client, path string) (WorkflowStatus, error) {
	isURL := strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://")
	byConvention := IsWorkflowPathByConvention(path)

	var content []byte
	var err error
	if isURL {
		content, err = FetchWorkflowContent(ctx, ghClient, path)
	} else {
		content, err = fetchRepoWorkflowContent(ctx, ghClient, path, byConvention)
	}
	if err != nil {
		switch {
		case !byConvention:
			klog.V(4).Infof("Failed to fetch content for %s: %v", path, err)
			return NotAWorkflow, err
		case errors.Is(err, ErrWorkflowNotFound):
			return WorkflowMissing, err
		case errors.Is(err, ErrWorkflowUnusable):
			return WorkflowUnusable, err
		default:
			return WorkflowUnresolved, err
		}
	}

	if byConvention || declaresWorkflowMode(content) {
		return WorkflowFound, nil
	}
	return NotAWorkflow, nil
}

// fetchRepoWorkflowContent reads a definition from the default branch of the
// repository ghClient is bound to, following its redirect if asked to.
func fetchRepoWorkflowContent(ctx context.Context, ghClient *github.Client, path string, followRedirect bool) ([]byte, error) {
	// Clean up leading dot slashes from path to match GitHub API format
	cleanPath := strings.TrimPrefix(path, "./")
	cleanPath = strings.TrimPrefix(cleanPath, "/")

	fetchCtx, cancel := context.WithTimeout(ctx, workflowFetchTimeout)
	contentStr, err := ghClient.FileContent(fetchCtx, cleanPath, "")
	cancel()
	if err != nil {
		if github.IsNotFound(err) {
			return nil, fmt.Errorf("%w: %w", ErrWorkflowNotFound, err)
		}
		return nil, err
	}
	if !followRedirect {
		return []byte(contentStr), nil
	}
	return ResolveWorkflowRedirect(ctx, ghClient, []byte(contentStr))
}

// declaresWorkflowMode looks for workflow mode metadata in the header or
// front-matter of a definition.
func declaresWorkflowMode(content []byte) bool {
	limit := 2000
	if len(content) < limit {
		limit = len(content)
	}
	header := string(content[:limit])
	return strings.Contains(header, "mode: workflow") || strings.Contains(header, "mode: \"workflow\"") || strings.Contains(header, "AGENT_MODE=workflow")
}

// DefaultWorkflowCooldown is a workflow's cooldown when its definition
// declares none.
const DefaultWorkflowCooldown = 10 * time.Minute

// GetWorkflowCooldown is the cooldown the workflow definition at path
// declares (DefaultWorkflowCooldown when it declares none, or one that does
// not parse), and an error when the definition cannot be read: a cooldown
// that is not known is not the default, which would step the workflow
// early.
func GetWorkflowCooldown(ctx context.Context, ghClient *github.Client, path string) (time.Duration, error) {
	defaultCooldown := DefaultWorkflowCooldown
	if path == "" {
		return defaultCooldown, nil
	}

	var content []byte
	var err error
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		content, err = FetchWorkflowContent(ctx, ghClient, path)
	} else {
		content, err = fetchRepoWorkflowContent(ctx, ghClient, path, true)
	}
	if err != nil {
		return 0, fmt.Errorf("reading the workflow %s for its cooldown: %w", path, err)
	}

	agentDef, err := ParseAgent(content)
	if err != nil || agentDef.Cooldown == "" {
		return defaultCooldown, nil
	}

	d, err := time.ParseDuration(agentDef.Cooldown)
	if err != nil {
		klog.Warningf("Failed to parse workflow cooldown duration %q: %v", agentDef.Cooldown, err)
		return defaultCooldown, nil
	}
	return d, nil
}
