package watch

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"
)

var (
	workflowURLRegex  = regexp.MustCompile(`(?:\s|^)(https?://[^\s\)]+(?:\.(?:md|txt|yaml)|/(?:workflows|agents)/)[^\s\)]*)`)
	workflowFileRegex = regexp.MustCompile(`(?:\s|^)(\.?\.?/?(?:\.?agents?|\.gemini)/[a-zA-Z0-9_\-\./]+)\b`)
)

// FindWorkflowPath extracts a workflow path or URL from a text body.
func FindWorkflowPath(body string) string {
	urlMatch := workflowURLRegex.FindStringSubmatch(body)
	if len(urlMatch) > 1 {
		return strings.TrimSpace(urlMatch[1])
	}

	matches := workflowFileRegex.FindStringSubmatch(body)
	if len(matches) > 1 {
		return matches[1]
	}
	return ""
}

// IsWorkflowDefinition determines if the given path/URL points to a workflow definition.
func IsWorkflowDefinition(ctx context.Context, ghClient *githubv39.Client, owner, repo, path string) bool {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		// 1. Path/URL convention check
		if strings.Contains(path, "/workflows/") || strings.Contains(path, "/agents/") {
			return true
		}

		// 2. Download and verify headers
		content, err := fetchWorkflowContent(ctx, ghClient, path)
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

// Slugify formats a string to be URL-friendly.
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

func assignedBotUser(issue *githubv39.Issue, botUsers []string) string {
	for _, assignee := range issue.Assignees {
		if assignee == nil || assignee.GetLogin() == "" {
			continue
		}
		for _, bot := range botUsers {
			if strings.EqualFold(assignee.GetLogin(), bot) {
				return assignee.GetLogin()
			}
		}
	}
	return ""
}

func issuePriority(issue *githubv39.Issue) string {
	for _, label := range issue.Labels {
		if label == nil || label.GetName() == "" {
			continue
		}
		name := label.GetName()
		if strings.HasPrefix(name, "priority/") {
			return strings.TrimPrefix(name, "priority/")
		}
	}
	return "medium"
}

func prPriority(prIssue *githubv39.Issue) string {
	return issuePriority(prIssue)
}

func workflowCooldown(ctx context.Context, ghClient *githubv39.Client, owner, repo, path string) time.Duration {
	defaultCooldown := 10 * time.Minute
	if path == "" {
		return defaultCooldown
	}

	var content []byte
	var err error
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		content, err = fetchWorkflowContent(ctx, ghClient, path)
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

func getReferencedIssues(pr *githubv39.PullRequest) map[int]bool {
	referenced := make(map[int]bool)

	// Check branch name
	if pr.GetHead().GetRef() != "" {
		re := regexp.MustCompile(`\d+`)
		for _, match := range re.FindAllString(pr.GetHead().GetRef(), -1) {
			if num, err := strconv.Atoi(match); err == nil {
				referenced[num] = true
			}
		}
	}

	// Check title and body
	re := regexp.MustCompile(`#(\d+)\b`)
	for _, text := range []string{pr.GetTitle(), pr.GetBody()} {
		for _, match := range re.FindAllStringSubmatch(text, -1) {
			if len(match) > 1 {
				if num, err := strconv.Atoi(match[1]); err == nil {
					referenced[num] = true
				}
			}
		}
	}

	return referenced
}

func syncReferencedIssueLabels(ctx context.Context, ghClient *githubv39.Client, owner, repo string, pr *githubv39.PullRequest, prIssue *githubv39.Issue) {
	var refIssues []*githubv39.Issue
	for refIssueNum := range getReferencedIssues(pr) {
		refIssue, _, err := ghClient.Issues.Get(ctx, owner, repo, refIssueNum)
		if err != nil {
			klog.Warningf("Failed to fetch referenced parent issue #%d for PR #%d: %v", refIssueNum, pr.GetNumber(), err)
			continue
		}
		refIssues = append(refIssues, refIssue)
	}

	allMissingLabels := getMissingLabelsForPR(prIssue.Labels, refIssues)

	if len(allMissingLabels) > 0 {
		klog.Infof("Adding inherited labels %v to PR #%d", allMissingLabels, pr.GetNumber())
		if _, _, err := ghClient.Issues.AddLabelsToIssue(ctx, owner, repo, pr.GetNumber(), allMissingLabels); err != nil {
			klog.Errorf("Failed to add labels %v to PR #%d: %v", allMissingLabels, pr.GetNumber(), err)
		}
	}
}

func getMissingLabelsForPR(prLabels []*githubv39.Label, refIssues []*githubv39.Issue) []string {
	prLabelsSet := make(map[string]bool)
	for _, label := range prLabels {
		if label.GetName() != "" {
			prLabelsSet[label.GetName()] = true
		}
	}

	var allMissingLabels []string
	missingLabelsSet := make(map[string]bool)

	for _, refIssue := range refIssues {
		if refIssue == nil {
			continue
		}

		for _, label := range refIssue.Labels {
			labelName := label.GetName()
			if labelName != "" && !prLabelsSet[labelName] && !missingLabelsSet[labelName] {
				missingLabelsSet[labelName] = true
				allMissingLabels = append(allMissingLabels, labelName)
			}
		}
	}

	return allMissingLabels
}

func hasLinkedPR(ctx context.Context, client *githubv39.Client, owner, repo string, issueNum int) (bool, error) {
	// 1. Try timeline check (quick and standard)
	timeline, _, err := client.Issues.ListIssueTimeline(ctx, owner, repo, issueNum, nil)
	if err == nil {
		for _, event := range timeline {
			if event.GetEvent() == "cross-referenced" && event.Source != nil {
				if event.Source.Issue != nil && event.Source.Issue.PullRequestLinks != nil {
					if event.Source.Issue.GetState() == "open" {
						return true, nil
					}
				}
			}
		}
	} else {
		klog.Warningf("Failed to list issue timeline for #%d: %v. Falling back to search API.", issueNum, err)
	}

	// 2. Fallback to Search API: search for open PRs referencing the issue number
	query := fmt.Sprintf("repo:%s/%s type:pr state:open \"%d\"", owner, repo, issueNum)
	opts := &githubv39.SearchOptions{
		ListOptions: githubv39.ListOptions{PerPage: 10},
	}
	result, _, err := client.Search.Issues(ctx, query, opts)
	if err != nil {
		return false, fmt.Errorf("failed to search PRs for issue #%d: %w", issueNum, err)
	}

	if result.GetTotal() > 0 {
		return true, nil
	}

	return false, nil
}

func isPRApprovedOrLGTM(pr *githubv39.PullRequest, prIssue *githubv39.Issue, reviews []*githubv39.PullRequestReview) bool {
	// 1. Check labels
	for _, label := range prIssue.Labels {
		if strings.EqualFold(label.GetName(), "lgtm") || strings.EqualFold(label.GetName(), "approved") {
			return true
		}
	}

	// 2. Check reviews
	hasApproved := false
	hasChangesRequested := false
	latestReviews := make(map[string]string)
	for _, r := range reviews {
		if r.GetUser() != nil && r.GetState() != "" {
			latestReviews[r.GetUser().GetLogin()] = r.GetState()
		}
	}
	for _, state := range latestReviews {
		if state == "APPROVED" {
			hasApproved = true
		} else if state == "CHANGES_REQUESTED" {
			hasChangesRequested = true
		}
	}

	return hasApproved && !hasChangesRequested
}

func shouldIgnoreUser(user *githubv39.User, githubLogin string, allowlistedBots []string) bool {
	if user == nil {
		return false
	}
	login := user.GetLogin()
	if strings.EqualFold(login, githubLogin) {
		return true // always ignore our own bot
	}

	loginLower := strings.ToLower(login)
	isBotUser := strings.EqualFold(user.GetType(), "Bot") ||
		strings.HasSuffix(loginLower, "[bot]") ||
		strings.HasSuffix(loginLower, "-bot") ||
		strings.HasSuffix(loginLower, "-robot") ||
		strings.Contains(loginLower, "prow")

	if isBotUser {
		// Check if it's in the allowlist
		for _, b := range allowlistedBots {
			if strings.EqualFold(login, b) {
				return false // DO NOT ignore (it is allowlisted)
			}
		}
		return true // ignore since it is not allowlisted
	}

	return false
}

func isPRTask(taskType string) bool {
	return taskType == "pr-investigate" || taskType == "pr-comments" || taskType == "pr-iterate"
}

func listAllCheckRuns(ctx context.Context, client *githubv39.Client, owner, repo, ref string) ([]*githubv39.CheckRun, error) {
	var allRuns []*githubv39.CheckRun
	opts := &githubv39.ListCheckRunsOptions{
		ListOptions: githubv39.ListOptions{
			PerPage: 200,
		},
	}
	for {
		runs, resp, err := client.Checks.ListCheckRunsForRef(ctx, owner, repo, ref, opts)
		if err != nil {
			return nil, err
		}
		allRuns = append(allRuns, runs.CheckRuns...)
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	// Deduplicate check runs by name, keeping only the latest run (highest ID)
	latestRuns := make(map[string]*githubv39.CheckRun)
	for _, run := range allRuns {
		name := run.GetName()
		if existing, ok := latestRuns[name]; ok {
			if run.GetID() > existing.GetID() {
				latestRuns[name] = run
			}
		} else {
			latestRuns[name] = run
		}
	}

	deduped := make([]*githubv39.CheckRun, 0, len(latestRuns))
	for _, run := range latestRuns {
		deduped = append(deduped, run)
	}
	return deduped, nil
}

func parseGitHubURL(urlStr string) (owner, repo, branch, path string, ok bool) {
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

func fetchWorkflowContent(ctx context.Context, ghClient *githubv39.Client, urlStr string) ([]byte, error) {
	if owner, repo, branch, path, ok := parseGitHubURL(urlStr); ok {
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

func addGitHubComment(ctx context.Context, client *githubv39.Client, owner, repo string, number int, body string) error {
	comment := &githubv39.IssueComment{Body: &body}
	_, _, err := client.Issues.CreateComment(ctx, owner, repo, number, comment)
	return err
}
