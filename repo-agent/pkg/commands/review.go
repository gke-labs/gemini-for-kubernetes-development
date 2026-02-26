package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bluekeyes/go-gitdiff/gitdiff"
	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repowatch/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/agentoutput"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/imagebuilder"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/llm"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/prompts"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/tokens"
	"github.com/google/go-github/v39/github"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
)

var (
	ReviewGVR = schema.GroupVersionResource{
		Group:    "agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "sandboxes",
	}
)

const DefaultMaxReviewFiles = 150

type ReviewCommand struct {
	WorkspaceDir      string
	TokensDir         string
	RepoURL           string
	UserDotfilesRepo  string
	CloneURL          string
	AgentName         string
	AgentPrompt       string
	DiffURL           string
	MaxReviewFiles    int
	ExpectedComments  int
	IgnoreFiles       []string
	SeverityThreshold string
	Extensions        []reviewv1alpha1.Extension

	// output
	TaskDir         string
	OutputGVR       *schema.GroupVersionResource
	OutputName      string
	OutputNamespace string
}

func (c *ReviewCommand) InitDefaults() {
	if c.OutputGVR == nil {
		if gvrResource := os.Getenv("AGENT_OUTPUT_GVR_RESOURCE"); gvrResource != "" {
			group := os.Getenv("AGENT_OUTPUT_GVR_GROUP")
			version := os.Getenv("AGENT_OUTPUT_GVR_VERSION")
			if group != "" && version != "" {
				gvr := schema.GroupVersionResource{
					Group:    group,
					Version:  version,
					Resource: gvrResource,
				}
				c.OutputGVR = &gvr
			}
		}
	}
	if c.OutputGVR == nil {
		c.OutputGVR = &ReviewGVR
	}
	if c.OutputName == "" {
		c.OutputName = os.Getenv("NAME")
	}
	if c.OutputNamespace == "" {
		c.OutputNamespace = os.Getenv("NAMESPACE")
	}
	if c.WorkspaceDir == "" {
		c.WorkspaceDir = "/workspaces"
	}
	if c.TaskDir == "" {
		c.TaskDir = os.Getenv("TASKDIR")
	}
	if c.TaskDir == "" {
		c.TaskDir = c.WorkspaceDir
	}
	if c.TokensDir == "" {
		c.TokensDir = "/tokens"
	}
	if c.RepoURL == "" {
		c.RepoURL = os.Getenv("GIT_HTML_URL")
	}
	if c.UserDotfilesRepo == "" {
		c.UserDotfilesRepo = os.Getenv("USER_DOTFILESREPO")
	}
	if c.CloneURL == "" {
		c.CloneURL = os.Getenv("GIT_CLONE_URL")
	}
	if c.AgentName == "" {
		c.AgentName = os.Getenv("AGENT_NAME")
	}
	if c.AgentPrompt == "" {
		c.AgentPrompt = os.Getenv("AGENT_PROMPT")
	}
	if c.AgentPrompt == "" {
		c.AgentPrompt = os.Getenv("prompt")
	}
	if c.AgentPrompt == "" {
		c.AgentPrompt = os.Getenv("PROMPT")
	}
	if c.DiffURL == "" {
		c.DiffURL = os.Getenv("GIT_DIFF_URL")
	}
	if c.MaxReviewFiles == 0 {
		maxReviewFilesStr := os.Getenv("MAX_REVIEW_FILES")
		if maxReviewFilesStr != "" {
			val, err := strconv.Atoi(maxReviewFilesStr)
			if err != nil {
				klog.Infof("Invalid MAX_REVIEW_FILES value '%s', using default %d: %v", maxReviewFilesStr, DefaultMaxReviewFiles, err)
				c.MaxReviewFiles = DefaultMaxReviewFiles
			} else {
				c.MaxReviewFiles = val
			}
		} else {
			c.MaxReviewFiles = DefaultMaxReviewFiles
		}
	}
	if c.ExpectedComments == 0 {
		expectedCommentsStr := os.Getenv("EXPECTED_COMMENTS")
		if expectedCommentsStr != "" {
			val, err := strconv.Atoi(expectedCommentsStr)
			if err != nil {
				klog.Infof("Invalid EXPECTED_COMMENTS value '%s', ignoring: %v", expectedCommentsStr, err)
			} else {
				c.ExpectedComments = val
			}
		}
	}
	if len(c.IgnoreFiles) == 0 {
		ignoreFilesStr := os.Getenv("IGNORE_FILES")
		if ignoreFilesStr != "" {
			c.IgnoreFiles = strings.Split(ignoreFilesStr, ",")
		}
	}
	if c.SeverityThreshold == "" {
		c.SeverityThreshold = os.Getenv("SEVERITY_THRESHOLD")
	}
	if len(c.Extensions) == 0 {
		extensionsJSON := os.Getenv("AGENT_LLM_EXTENSIONS")
		if extensionsJSON != "" {
			var extensions []reviewv1alpha1.Extension
			if err := json.Unmarshal([]byte(extensionsJSON), &extensions); err != nil {
				klog.Infof("Warning: failed to unmarshal AGENT_LLM_EXTENSIONS: %v", err)
			} else {
				c.Extensions = extensions
			}
		}
	}
}

func BuildReviewCommand() *cobra.Command {
	reviewCommand := ReviewCommand{}
	cmd := &cobra.Command{
		Use:   "review",
		Short: "Run the review agent",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("review command does not take any arguments")
			}
			reviewCommand.InitDefaults()
			return reviewCommand.Run(cmd.Context())
		},
	}

	cmd.Flags().StringVar(&reviewCommand.RepoURL, "repo-url", os.Getenv("GIT_HTML_URL"), "Git HTML URL")
	cmd.Flags().StringVar(&reviewCommand.UserDotfilesRepo, "user-dotfiles-repo", os.Getenv("USER_DOTFILESREPO"), "User dotfiles repo")
	cmd.Flags().StringVar(&reviewCommand.CloneURL, "clone-url", os.Getenv("GIT_CLONE_URL"), "Git clone URL")
	cmd.Flags().StringVar(&reviewCommand.AgentName, "agent-name", os.Getenv("AGENT_NAME"), "Agent name")
	cmd.Flags().StringVar(&reviewCommand.AgentPrompt, "agent-prompt", os.Getenv("AGENT_PROMPT"), "Agent prompt")
	cmd.Flags().StringVar(&reviewCommand.DiffURL, "diff-url", os.Getenv("GIT_DIFF_URL"), "Git diff URL")
	cmd.Flags().IntVar(&reviewCommand.MaxReviewFiles, "max-review-files", 0, "Max review files")
	cmd.Flags().IntVar(&reviewCommand.ExpectedComments, "expected-comments", 0, "Expected number of comments")
	cmd.Flags().StringSliceVar(&reviewCommand.IgnoreFiles, "ignore-files", nil, "Comma separated list of glob patterns to ignore")
	cmd.Flags().StringVar(&reviewCommand.SeverityThreshold, "severity-threshold", os.Getenv("SEVERITY_THRESHOLD"), "Severity threshold for review comments")

	return cmd
}

func (c *ReviewCommand) taskPath(name string, args ...interface{}) string {
	// Ensure the task path is correctly joined
	file := fmt.Sprintf(name, args...)
	return filepath.Join(c.TaskDir, file)
}

func (c *ReviewCommand) Run(ctx context.Context) error {
	log := klog.FromContext(ctx)

	ao, err := agentoutput.New(*c.OutputGVR, c.OutputName, c.OutputNamespace)
	if err != nil {
		log.Error(err, "failed to create k8s client: %w", err)
		return err
	}

	updateState := func(state, message string) {
		err := ao.SetAgentState(ctx, state, message)
		if err != nil {
			log.Error(err, "updating agent state failed")
		}
	}

	updateState("reviewing", "")

	// -------------------------------------------------------------------------
	// 1. Setup Environment (Clone Repo & Dotfiles) - copied/adapted from dev.go
	// -------------------------------------------------------------------------
	if c.RepoURL == "" {
		return fmt.Errorf("GIT_HTML_URL (or --repo-url) not set")
	}

	parts := strings.Split(strings.TrimPrefix(c.RepoURL, "https://github.com/"), "/")
	if len(parts) < 2 {
		return fmt.Errorf("invalid GIT_HTML_URL format: %s", c.RepoURL)
	}
	repoDir := filepath.Join(c.WorkspaceDir, parts[1])

	ib := imagebuilder.ImageBuilder{
		DotFilesRepo: c.UserDotfilesRepo,
		CloneURL:     c.CloneURL,
		Destination:  repoDir,
	}

	// If repoDir doesnt exist, we need to clone it
	if _, err := os.Stat(repoDir); os.IsNotExist(err) {
		log.Info("Cloning repo", "url", c.CloneURL, "dest", repoDir)
		if err := ib.CloneRepo(ctx); err != nil {
			updateState("error", fmt.Sprintf("cloning repo failed: %v", err))
			return fmt.Errorf("cloning repo failed: %w", err)
		}
	}

	if err := ib.InstallDotfilesRepo(ctx); err != nil {
		log.Error(err, "installing dotfiles repo", "repo", ib.DotFilesRepo)
	}

	// -------------------------------------------------------------------------
	// 2. Run Review Agent
	// -------------------------------------------------------------------------
	log.Info("Review", "AGENT_NAME", c.AgentName)
	rawAgentPrompt := c.AgentPrompt

	// save the incoming prompt
	if err := os.WriteFile(c.taskPath("agent-prompt.txt"), []byte(rawAgentPrompt), 0644); err != nil {
		log.Error(err, "Failed to write prompt to file")
	}

	var accumulatedAgentOutput models.ReviewAgentOutput
	var diffSizeLabel string
	var diffFiles []*gitdiff.File
	diffURL := c.DiffURL
	var expectedComments int
	// Check if diffURL begins with https://github.com/
	if !bytes.HasPrefix([]byte(diffURL), []byte("https://github.com/")) {
		return fmt.Errorf("GIT_DIFF_URL (or --diff-url) must start with https://github.com/")
	}

	if diffURL != "" {
		log.Info("Downloading and parsing diff", "url", diffURL)
		diffFiles, err = parseDiffFromURL(diffURL)
		if err != nil {
			updateState("error", fmt.Sprintf("failed to parse diff: %v", err))
			return fmt.Errorf("failed to parse diff from URL: %v", err)
		}

		// Filter files based on ignore patterns and generated files
		diffFiles = filterDiffFiles(repoDir, diffFiles, c.IgnoreFiles)

		if len(diffFiles) > c.MaxReviewFiles {
			errStr := fmt.Sprintf("Too many files to review: %d (max %d)", len(diffFiles), c.MaxReviewFiles)
			updateState("Error: Too many files", errStr)
			return fmt.Errorf("%s", errStr)
		}

		diffSize := getDiffSize(repoDir, diffFiles, c.IgnoreFiles)
		diffSizeLabel = fmt.Sprintf("size/%s", diffSize)
		log.Info("Adding diff size label", "label", diffSizeLabel)
		// Initialize accumulatedAgentOutput with the size label
		if err := ao.AddAgentLabel(ctx, []string{diffSizeLabel}); err != nil {
			log.Error(err, "Failed to add size label")
		}
		if c.ExpectedComments > 0 {
			expectedComments = c.ExpectedComments
		} else {
			expectedComments = sizeToComments[diffSize]
		}
		log.Info("Diff size categorized", "size", diffSize, "expectedComments", expectedComments)
	} else {
		return fmt.Errorf("GIT_DIFF_URL not set, skipping diff-based validation")
	}

	provider, err := llm.NewLLMProvider(llm.ProviderConfig{
		Name:                 c.AgentName,
		OutputStartIndicator: "note:",
		WorkspacesDir:        c.WorkspaceDir,
		TokensDir:            c.TokensDir,
		RepoDir:              repoDir,
		Extensions:           c.Extensions,
	})
	if err != nil {
		updateState("error", err.Error())
		return err
	}

	if err := provider.Setup(); err != nil {
		updateState("error", err.Error())
		return err
	}

	// Get existing comments
	githubToken := tokens.GetGitHubToken()
	if githubToken == "" {
		return fmt.Errorf("GitHub token not found in environment variables (tried MANUAL_PAT, OAUTH_PAT, and GITHUB_TOKEN)")
	}
	client := clients.NewGitHubClient(ctx, githubToken)

	// Reuse parts from earlier, but need to be sure about format for PR
	// c.RepoURL: https://github.com/owner/repo/pull/123
	if len(parts) < 4 {
		return fmt.Errorf("invalid GIT_HTML_URL for review: %s", c.RepoURL)
	}
	owner := parts[0]
	repo := parts[1]
	prNumber, err := strconv.Atoi(parts[3])
	if err != nil {
		return fmt.Errorf("failed to parse PR number from GIT_HTML_URL: %w", err)
	}

	pr, _, err := client.PullRequests.Get(ctx, owner, repo, prNumber)
	if err != nil {
		updateState("error", err.Error())
		return fmt.Errorf("failed to get pull request: %w", err)
	}

	model := prompts.ReviewPromptModel{
		PullRequest: *pr,
		Prompt:      rawAgentPrompt,
		IgnoreFiles: c.IgnoreFiles,
	}
	expandedPrompt, err := prompts.ExpandReviewPrompt(model)
	if err != nil {
		updateState("error", err.Error())
		return fmt.Errorf("failed to expand review prompt: %w", err)
	}

	agentPrompt := fmt.Sprintf("%s \n\n Try generating at least %d review comments", expandedPrompt, expectedComments)

	existingComments, err := getExistingComments(ctx, client, owner, repo, prNumber)
	if err != nil {
		updateState("error", err.Error())
		return fmt.Errorf("failed to get existing comments: %w", err)
	}
	log.Info("Found existing comments", "count", len(existingComments))

	maxRuns := 3
	maxSuccessfulRuns := 2
	successfulRuns := 0

	for i := 0; i < maxRuns; i++ {
		log.Info("Running Agent", "agent", c.AgentName, "attempt", i+1, "maxRuns", maxRuns, "successfulRuns", successfulRuns)

		if successfulRuns >= maxSuccessfulRuns {
			log.Info("Stopping because max successful runs reached", "maxSuccessfulRuns", maxSuccessfulRuns)
			break
		}
		if accumulatedAgentOutput.Review != nil && len(accumulatedAgentOutput.Review.Comments) >= expectedComments {
			log.Info("Stopping because expected number of comments was met", "expectedComments", expectedComments)
			break
		}

		currentPrompt := agentPrompt
		currentPrompt += "\n\nDon't repeat comments you've already made in previous runs or that already exist on the PR."
		if accumulatedAgentOutput.Review != nil && len(accumulatedAgentOutput.Review.Comments) > 0 {
			previousReviews, err := yaml.Marshal(accumulatedAgentOutput)
			if err != nil {
				log.Info("failed to marshal previous reviews, continuing without them", "error", err)
			} else {
				currentPrompt += "\n\nReview comments made in previous runs:\n```yaml\n" + string(previousReviews) + "\n```\n"
			}
		}
		if len(existingComments) > 0 {
			existingReviews, err := yaml.Marshal(existingComments)
			if err != nil {
				log.Info("failed to marshal existing reviews, continuing without them", "error", err)
			} else {
				currentPrompt += "\n\nExisting comments on the PR:\n```yaml\n" + string(existingReviews) + "\n```\n"
			}
		}

		// Expand commands in the prompt
		currentPrompt, err = provider.ExpandPrompt(currentPrompt)
		if err != nil {
			log.Info("warning: failed to expand commands in prompt", "error", err)
		}

		// Write current prompt to file for debugging
		promptFilename := c.taskPath("agent-prompt-run%d.txt", i+1)
		if err := os.WriteFile(promptFilename, []byte(currentPrompt), 0644); err != nil {
			log.Error(err, "Failed to write agent prompt to file", "filename", promptFilename)
		} else {
			log.Info("Wrote agent prompt", "filename", promptFilename)
		}

		// RUN THE AGENT
		output, err := provider.Run(currentPrompt)
		if err != nil {
			var quotaErr *llm.QuotaError
			if errors.As(err, &quotaErr) {
				updateState("QUOTA ERROR", err.Error())
				log.Error(err, "Agent run failed due to quota.")
				return err
			}
			log.Error(err, "Agent run failed. Continuing...")
			time.Sleep(10 * time.Second)
			continue
		}

		// Write output to file for debugging, regardless of validation result.
		filename := c.taskPath("agent-output-run%d.txt", i+1)
		if err := os.WriteFile(filename, output, 0644); err != nil {
			log.Error(err, "Failed to write agent output", "filename", filename)
		} else {
			log.Info("Wrote agent output", "filename", filename)
		}

		var agentOutput models.ReviewAgentOutput
		if err := yaml.Unmarshal(output, &agentOutput); err != nil {
			log.Info("Agent output validation failed: failed to unmarshal yaml. Continuing...", "error", err)
			time.Sleep(5 * time.Second)
			continue
		}

		if err = validateAgentOutput(ctx, &agentOutput, diffFiles); err != nil {
			log.Info("Agent output validation failed. Continuing...", "error", err)
			time.Sleep(5 * time.Second)
			continue
		}

		log.Info("Agent run and validation successful.")
		successfulRuns++

		if accumulatedAgentOutput.Review == nil {
			// Save existing labels (e.g. size/S)
			existingLabels := accumulatedAgentOutput.Labels
			accumulatedAgentOutput = agentOutput
			// Restore/Merge labels
			accumulatedAgentOutput.Labels = append(accumulatedAgentOutput.Labels, existingLabels...)
			accumulatedAgentOutput.Labels = uniqueStrings(accumulatedAgentOutput.Labels)
		} else {
			for _, newComment := range agentOutput.Review.Comments {
				if newComment == nil || newComment.Path == nil || newComment.Line == nil || newComment.Body == nil {
					continue
				}
				cleanPath := strings.TrimPrefix(*newComment.Path, "b/")

				if IsGeneratedFile(repoDir, cleanPath) {
					log.Info("Filtering out comment on generated file", "file", *newComment.Path)
					continue
				}

				if shouldIgnoreFile(cleanPath, c.IgnoreFiles) {
					log.Info("Filtering out comment on ignored file", "file", *newComment.Path)
					continue
				}

				if getSeverityLevel(newComment.Severity) < getSeverityLevel(c.SeverityThreshold) {
					log.Info("Filtering out comment below severity threshold", "file", *newComment.Path, "severity", newComment.Severity, "threshold", c.SeverityThreshold)
					continue
				}

				if !isDuplicateCommentExact(newComment, existingComments, accumulatedAgentOutput.Review.Comments) {
					accumulatedAgentOutput.Review.Comments = append(accumulatedAgentOutput.Review.Comments, newComment)
				} else {
					log.Info("Filtering out duplicate comment", "file", *newComment.Path, "line", *newComment.Line)
				}
			}
			if agentOutput.Note != "" {
				accumulatedAgentOutput.Note += "\n--- " + agentOutput.Note
			}
			if agentOutput.Review.Body != nil && *agentOutput.Review.Body != "" {
				if accumulatedAgentOutput.Review.Body == nil {
					accumulatedAgentOutput.Review.Body = agentOutput.Review.Body
				} else {
					newBody := *accumulatedAgentOutput.Review.Body + "\n--- " + *agentOutput.Review.Body
					accumulatedAgentOutput.Review.Body = &newBody
				}
			}
			if len(agentOutput.Labels) > 0 {
				accumulatedAgentOutput.Labels = append(accumulatedAgentOutput.Labels, agentOutput.Labels...)
				accumulatedAgentOutput.Labels = uniqueStrings(accumulatedAgentOutput.Labels)
			}
		}
	}

	if successfulRuns == 0 {
		updateState("error", fmt.Sprintf("agent failed to produce any valid output after %d attempts", maxRuns))
		return fmt.Errorf("agent failed to produce any valid output after %d attempts", maxRuns)
	}

	log.Info("Finished agent runs", "successfulRuns", successfulRuns, "totalComments", len(accumulatedAgentOutput.Review.Comments))

	// If more than one successful run, accumalatedAgentOutput has duplicated text in Note and Review.Body, so dedupe and combine them.
	if successfulRuns > 1 {
		log.Info("Deduplicating and combining agent output text from multiple runs.")
		combinedNote, err := dedupeAndCombineText(provider, accumulatedAgentOutput.Note)
		if err != nil {
			log.Info("Failed to dedupe and combine Note. Using original Note.", "error", err)
			combinedNote = accumulatedAgentOutput.Note
		} else {
			log.Info("Successfully deduped and combined Note.")
		}
		accumulatedAgentOutput.Note = combinedNote

		combinedBody, err := dedupeAndCombineText(provider, *accumulatedAgentOutput.Review.Body)
		if err != nil {
			log.Info("Failed to dedupe and combine Body. Using original Body.", "error", err)
		} else {
			accumulatedAgentOutput.Review.Body = &combinedBody
			log.Info("Successfully deduped and combined Body.")
		}
	}

	finalOutput, err := yaml.Marshal(&accumulatedAgentOutput)
	if err != nil {
		updateState("error", fmt.Sprintf("failed to re-marshal agent output: %v", err))
		return fmt.Errorf("failed to re-marshal agent output: %w", err)
	}

	filename := c.taskPath("agent-output.txt")
	if err := os.WriteFile(filename, finalOutput, 0644); err != nil {
		updateState("error", fmt.Sprintf("failed to write agent output: %v", err))
		return fmt.Errorf("failed to write agent output to %s: %v", filename, err)
	}

	if err := ao.SetAgentDraft(ctx, string(finalOutput)); err != nil {
		log.Error(err, "Failed to set agent draft")
		return fmt.Errorf("failed to write agent output to %s: %v", filename, err)
	}

	accumulatedAgentOutput.Labels = append(accumulatedAgentOutput.Labels, diffSizeLabel)
	if err := ao.AddAgentLabel(ctx, accumulatedAgentOutput.Labels); err != nil {
		log.Error(err, "Failed to add agent labels")
	}
	log.Info("Wrote agent output", "filename", filename)

	updateState("review ready", "")
	return nil
}

func uniqueStrings(input []string) []string {
	return sets.NewString(input...).List()
}

func getExistingComments(ctx context.Context, client *github.Client, owner, repo string, prNumber int) ([]*github.PullRequestComment, error) {
	var allComments []*github.PullRequestComment
	opts := &github.PullRequestListCommentsOptions{
		ListOptions: github.ListOptions{PerPage: 100},
	}
	for {
		comments, resp, err := client.PullRequests.ListComments(ctx, owner, repo, prNumber, opts)
		if err != nil {
			return nil, err
		}
		allComments = append(allComments, comments...)
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return allComments, nil
}

// TODO improve duplicate detection to fuzzy matching
func isDuplicateCommentExact(newComment *models.DraftReviewComment, existingComments []*github.PullRequestComment, accumulatedComments []*models.DraftReviewComment) bool {
	for _, existingComment := range existingComments {
		if existingComment == nil {
			continue
		}
		if *newComment.Path == *existingComment.Path &&
			*newComment.Line == *existingComment.Line &&
			*newComment.Body == *existingComment.Body {
			return true
		}
	}
	for _, existingComment := range accumulatedComments {
		if *newComment.Path == *existingComment.Path &&
			*newComment.Line == *existingComment.Line &&
			*newComment.Body == *existingComment.Body {
			return true
		}
	}
	return false
}

func validateAgentOutput(ctx context.Context, agentOutput *models.ReviewAgentOutput, diffFiles []*gitdiff.File) error {
	log := klog.FromContext(ctx)
	if agentOutput.Review == nil {
		return fmt.Errorf("'review' field is missing from yaml output")
	}

	if agentOutput.Review.Body == nil || *agentOutput.Review.Body == "" {
		return fmt.Errorf("'review.body' field is missing or empty")
	}

	if agentOutput.Review.Comments == nil {
		return fmt.Errorf("'review.comments' field is missing")
	}

	validComments := []*models.DraftReviewComment{}
	for _, comment := range agentOutput.Review.Comments {
		if isCommentValid(comment, diffFiles) {
			validComments = append(validComments, comment)
		} else {
			if comment.Path != nil && comment.Line != nil {
				log.Info("Filtering out invalid comment", "file", *comment.Path, "line", *comment.Line)
			} else {
				log.Info("Filtering out invalid comment with missing path or line")
			}
		}
	}
	agentOutput.Review.Comments = validComments

	log.Info("YAML validation successful.")
	return nil
}

func isCommentValid(comment *models.DraftReviewComment, diffFiles []*gitdiff.File) bool {
	if comment.Path == nil || comment.Line == nil {
		return false // Invalid comment if path or line is missing
	}
	side := "RIGHT"
	if comment.Side != nil {
		side = *comment.Side
	}
	for _, file := range diffFiles {
		if file.NewName == *comment.Path {
			for _, fragment := range file.TextFragments {
				if side == "RIGHT" {
					if fragment.NewPosition <= int64(*comment.Line) && int64(*comment.Line) <= fragment.NewPosition+fragment.NewLines {
						return true
					}
				} else {
					if fragment.OldPosition <= int64(*comment.Line) && int64(*comment.Line) <= fragment.OldPosition+fragment.OldLines {
						return true
					}
				}
			}
		}
	}
	return false
}

var sizeToComments = map[string]int{
	"XS":  2,
	"S":   4,
	"M":   6,
	"L":   10,
	"XL":  15,
	"XXL": 20,
}

// getDiffSize categorizes the diff based on the total number of lines changed.
func getDiffSize(repoDir string, files []*gitdiff.File, ignoreFiles []string) string {
	var totalLinesChanged int64
	for _, file := range files {
		// Git diffs usually use a/ and b/ prefixes
		path := strings.TrimPrefix(file.NewName, "b/")

		if IsGeneratedFile(repoDir, path) {
			continue
		}

		if shouldIgnoreFile(path, ignoreFiles) {
			continue
		}

		for _, fragment := range file.TextFragments {
			totalLinesChanged += fragment.LinesAdded
			totalLinesChanged += fragment.LinesDeleted
		}
	}

	switch {
	case totalLinesChanged <= 9:
		return "XS"
	case totalLinesChanged <= 29:
		return "S"
	case totalLinesChanged <= 99:
		return "M"
	case totalLinesChanged <= 499:
		return "L"
	case totalLinesChanged <= 999:
		return "XL"
	default:
		return "XXL"
	}
}

func parseDiffFromURL(url string) ([]*gitdiff.File, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to download diff: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to download diff: status code %d", resp.StatusCode)
	}

	files, _, err := gitdiff.Parse(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to parse diff: %w", err)
	}

	return files, nil
}

func dedupeAndCombineText(provider llm.Provider, text string) (string, error) {
	// We get text with sections separated by '---'
	prompt := fmt.Sprintf("The following text contains multiple sections separated by '---'. Please deduplicate and combine them into a single coherent section of text. Return only the result.\n\n%s", text)

	output, err := provider.Run(prompt)
	if err != nil {
		var quotaErr *llm.QuotaError
		if errors.As(err, &quotaErr) {
			return "", quotaErr
		}
		return "", err
	}

	return string(output), nil
}

func shouldIgnoreFile(path string, ignorePatterns []string) bool {
	for _, pattern := range ignorePatterns {
		// Special case for recursive directory matching with ** at the end
		if strings.HasSuffix(pattern, "/**") {
			prefix := strings.TrimSuffix(pattern, "**")
			if strings.HasPrefix(path, prefix) {
				return true
			}
		}

		// If the pattern doesn't contain a separator, match against the file name
		if !strings.Contains(pattern, "/") {
			matched, err := filepath.Match(pattern, filepath.Base(path))
			if err == nil && matched {
				return true
			}
		}
		// Match against the full path
		matched, err := filepath.Match(pattern, path)
		if err == nil && matched {
			return true
		}
	}
	return false
}

func filterDiffFiles(repoDir string, diffFiles []*gitdiff.File, ignoreFiles []string) []*gitdiff.File {
	var filteredDiffFiles []*gitdiff.File
	for _, file := range diffFiles {
		// gitdiff usually uses a/ and b/ prefixes
		path := strings.TrimPrefix(file.NewName, "b/")

		if IsGeneratedFile(repoDir, path) {
			klog.Infof("Filtering out generated file: %s", path)
			continue
		}

		if shouldIgnoreFile(path, ignoreFiles) {
			klog.Infof("Filtering out ignored file: %s", path)
			continue
		}
		filteredDiffFiles = append(filteredDiffFiles, file)
	}
	return filteredDiffFiles
}

func getSeverityLevel(severity string) int {
	switch strings.ToLower(severity) {
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 2 // Default to medium if not specified or unknown
	}
}
