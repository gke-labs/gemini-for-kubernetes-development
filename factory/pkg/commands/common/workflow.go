package common

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"gopkg.in/yaml.v3"
	"k8s.io/klog/v2"
)

type AgentDefinition struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Schedule    string `yaml:"schedule"`
	SkipPR      bool   `yaml:"skipPR,omitempty"`
	Mode        string `yaml:"mode,omitempty"`
	Cooldown    string `yaml:"cooldown,omitempty"`
	Prompt      string `yaml:"-"`
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

func FetchWorkflowContent(ctx context.Context, ghClient *githubv39.Client, urlStr string) ([]byte, error) {
	urlStr = SanitizeWorkflowPath(urlStr)
	if owner, repo, branch, path, ok := ParseGitHubURL(urlStr); ok {
		klog.Infof("Fetching agent from GitHub repository %s/%s at branch/ref %s, path %s", owner, repo, branch, path)
		fileContent, _, _, err := ghClient.Repositories.GetContents(ctx, owner, repo, path, &githubv39.RepositoryContentGetOptions{Ref: branch})
		if err != nil {
			return nil, fmt.Errorf("fetching content from GitHub repo: %w", err)
		}
		if fileContent == nil {
			return nil, fmt.Errorf("content is nil (possibly a directory or submodule)")
		}
		contentStr, err := fileContent.GetContent()
		if err != nil {
			return nil, fmt.Errorf("decoding GitHub content: %w", err)
		}
		return []byte(contentStr), nil
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

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP GET returned status %d", resp.StatusCode)
	}

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func IsWorkflowDefinition(ctx context.Context, ghClient *githubv39.Client, owner, repo, path string) bool {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		// 1. Path/URL convention check
		if strings.Contains(path, "/workflows/") || strings.Contains(path, "/agents/") {
			return true
		}

		// 2. Download and verify headers
		content, err := FetchWorkflowContent(ctx, ghClient, path)
		if err != nil {
			klog.V(4).Infof("Failed to fetch content from workflow URL %s: %v", path, err)
			return false
		}

		limit := 2000
		if len(content) < limit {
			limit = len(content)
		}
		header := string(content[:limit])
		if strings.Contains(header, "mode: workflow") || strings.Contains(header, "mode: \"workflow\"") || strings.Contains(header, "AGENT_MODE=workflow") {
			return true
		}
		return false
	}

	// 1. Directory convention: any path containing "/workflows/" is treated as a workflow
	if strings.Contains(path, "/workflows/") {
		return true
	}

	// Clean up leading dot slashes from path to match GitHub API format
	cleanPath := strings.TrimPrefix(path, "./")
	cleanPath = strings.TrimPrefix(cleanPath, "/")

	// 2. Fetch remote content from GitHub and search for keywords/metadata
	fileContent, _, _, err := ghClient.Repositories.GetContents(ctx, owner, repo, cleanPath, &githubv39.RepositoryContentGetOptions{})
	if err != nil {
		klog.V(4).Infof("Failed to get content for %s: %v", cleanPath, err)
		return false
	}
	if fileContent == nil {
		klog.V(4).Infof("Content is nil for %s (possibly a directory or submodule)", cleanPath)
		return false
	}
	content, err := fileContent.GetContent()
	if err != nil {
		return false
	}

	limit := 2000
	if len(content) < limit {
		limit = len(content)
	}
	header := content[:limit]

	// Look for mode: workflow metadata in header or front-matter
	if strings.Contains(header, "mode: workflow") || strings.Contains(header, "mode: \"workflow\"") || strings.Contains(header, "AGENT_MODE=workflow") {
		return true
	}

	return false
}

func GetWorkflowCooldown(ctx context.Context, ghClient *githubv39.Client, owner, repo, path string) time.Duration {
	defaultCooldown := 10 * time.Minute
	if path == "" {
		return defaultCooldown
	}

	var content []byte
	var err error
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		content, err = FetchWorkflowContent(ctx, ghClient, path)
	} else {
		cleanPath := strings.TrimPrefix(path, "./")
		cleanPath = strings.TrimPrefix(cleanPath, "/")
		var fileContent *githubv39.RepositoryContent
		fileContent, _, _, err = ghClient.Repositories.GetContents(ctx, owner, repo, cleanPath, &githubv39.RepositoryContentGetOptions{})
		if err == nil {
			var contentStr string
			contentStr, err = fileContent.GetContent()
			content = []byte(contentStr)
		}
	}
	if err != nil {
		return defaultCooldown
	}

	agentDef, err := ParseAgent(content)
	if err != nil || agentDef.Cooldown == "" {
		return defaultCooldown
	}

	d, err := time.ParseDuration(agentDef.Cooldown)
	if err != nil {
		klog.Warningf("Failed to parse workflow cooldown duration %q: %v", agentDef.Cooldown, err)
		return defaultCooldown
	}
	return d
}
