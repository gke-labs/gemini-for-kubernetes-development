package main

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/bluekeyes/go-gitdiff/gitdiff"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/llm"
	"github.com/google/go-github/v39/github"
	"gopkg.in/yaml.v3"
)

// AgentOutput defines the structure for the agent's YAML output.
type AgentOutput struct {
	Note   string                           `yaml:"note"`
	Review *github.PullRequestReviewRequest `yaml:"review"`
}

func main() {
	cmdCodeSrv, err := startCodeServer()
	if err != nil {
		log.Fatalf("failed to start code-server: %v", err)
	}
	defer func() {
		if cmdCodeSrv.Process != nil {
			_ = cmdCodeSrv.Process.Kill()
		}
	}()

	err = runReview()
	if err != nil {
		log.Fatalf("failed reviewing: %v", err)
	}

	err = cmdCodeSrv.Wait()
	if err != nil {
		log.Printf("Code Server exited with error: %v", err)
	} else {
		log.Println("Code Server exited with no error")
	}
}

func runReview() error {
	agentName := os.Getenv("AGENT_NAME")
	log.Printf("Review with AGENT_NAME: %s", agentName)

	// save the incoming prompt
	if err := os.WriteFile("../agent-prompt.txt", []byte(os.Getenv("AGENT_PROMPT")), 0644); err != nil {
		log.Printf("Failed to write prompt to file: %v", err)
	}

	var diffFiles []*gitdiff.File
	var err error
	diffURL := os.Getenv("GIT_DIFF_URL")
	var expectedComments int
	// Check if diffURL beings with https://github.com/
	if !bytes.HasPrefix([]byte(diffURL), []byte("https://github.com/")) {
		return fmt.Errorf("GIT_DIFF_URL must start with https://github.com/")
	}
	if diffURL != "" {
		log.Printf("Downloading and parsing diff from %s", diffURL)
		diffFiles, err = parseDiffFromURL(diffURL)
		if err != nil {
			return fmt.Errorf("failed to parse diff from URL: %v", err)
		}
		diffSize := getDiffSize(diffFiles)
		expectedComments = sizeToComments[diffSize]
		log.Printf("Diff size categorized as %s, expecting up to %d comments.", diffSize, expectedComments)
	} else {
		return fmt.Errorf("GIT_DIFF_URL not set, skipping diff-based validation")
	}

	agentPrompt := os.Getenv("AGENT_PROMPT")
	agentPrompt = fmt.Sprintf("%s \n\n Try generating at least %d review comments", agentPrompt, expectedComments)

	provider, err := llm.NewLLMProvider(agentName)
	if err != nil {
		return err
	}
	provider.AddPostProcessor(llm.StripYAMLMarkers)

	if err := provider.Setup("/workspaces", "/tokens"); err != nil {
		return err
	}

	var accumulatedAgentOutput AgentOutput
	maxRuns := 10
	maxSuccessfulRuns := 5
	successfulRuns := 0

	for i := 0; i < maxRuns; i++ {
		log.Printf("Running Agent %s (attempt %d/%d, successful runs %d)", agentName, i+1, maxRuns, successfulRuns)

		if successfulRuns >= maxSuccessfulRuns {
			log.Printf("Stopping because max successful runs (%d) reached.", maxSuccessfulRuns)
			break
		}
		if accumulatedAgentOutput.Review != nil && len(accumulatedAgentOutput.Review.Comments) >= expectedComments {
			log.Printf("Stopping because expected number of comments (%d) was met.", expectedComments)
			break
		}

		currentPrompt := agentPrompt
		if accumulatedAgentOutput.Review != nil && len(accumulatedAgentOutput.Review.Comments) > 0 {
			previousReviews, err := yaml.Marshal(accumulatedAgentOutput)
			if err != nil {
				log.Printf("failed to marshal previous reviews, continuing without them: %v", err)
			} else {
				currentPrompt = fmt.Sprintf("%s\n\nHere are the reviews generated so far:\n```yaml\n%s\n```\nPlease generate new, unique review comments that are not duplicates of the ones above.", agentPrompt, string(previousReviews))
			}
		}

		output, err := provider.Run(currentPrompt)
		if err != nil {
			log.Printf("Agent run failed: %v. Continuing...", err)
			time.Sleep(10 * time.Second)
			continue
		}

		// Write output to file for debugging, regardless of validation result.
		filename := fmt.Sprintf("../agent-output-run%d.txt", i+1)
		if err := os.WriteFile(filename, output, 0644); err != nil {
			log.Printf("Failed to write agent output to %s: %v", filename, err)
		} else {
			log.Printf("Wrote agent output to %s", filename)
		}

		var agentOutput AgentOutput
		if err := yaml.Unmarshal(output, &agentOutput); err != nil {
			log.Printf("Agent output validation failed: failed to unmarshal yaml: %v. Continuing...", err)
			time.Sleep(5 * time.Second)
			continue
		}

		if err = validateAgentOutput(&agentOutput, diffFiles); err != nil {
			log.Printf("Agent output validation failed: %v. Continuing...", err)
			time.Sleep(5 * time.Second)
			continue
		}

		log.Println("Agent run and validation successful.")
		successfulRuns++

		if accumulatedAgentOutput.Review == nil {
			accumulatedAgentOutput = agentOutput
		} else {
			accumulatedAgentOutput.Review.Comments = append(accumulatedAgentOutput.Review.Comments, agentOutput.Review.Comments...)
			if agentOutput.Note != "" {
				accumulatedAgentOutput.Note += "\n---\n" + agentOutput.Note
			}
			if agentOutput.Review.Body != nil && *agentOutput.Review.Body != "" {
				if accumulatedAgentOutput.Review.Body == nil {
					accumulatedAgentOutput.Review.Body = agentOutput.Review.Body
				} else {
					newBody := *accumulatedAgentOutput.Review.Body + "\n---\n" + *agentOutput.Review.Body
					accumulatedAgentOutput.Review.Body = &newBody
				}
			}
		}
	}

	if successfulRuns == 0 {
		return fmt.Errorf("agent failed to produce any valid output after %d attempts", maxRuns)
	}

	log.Printf("Finished agent runs. Total successful runs: %d. Total comments: %d", successfulRuns, len(accumulatedAgentOutput.Review.Comments))

	finalOutput, err := yaml.Marshal(&accumulatedAgentOutput)
	if err != nil {
		return fmt.Errorf("failed to re-marshal agent output: %w", err)
	}

	filename := "../agent-output.txt"
	if err := os.WriteFile(filename, finalOutput, 0644); err != nil {
		return fmt.Errorf("failed to write agent output to %s: %v", filename, err)
	}
	log.Printf("Wrote agent output to %s", filename)
	return nil // Success
}

func validateAgentOutput(agentOutput *AgentOutput, diffFiles []*gitdiff.File) error {
	if agentOutput.Review == nil {
		return fmt.Errorf("'review' field is missing from yaml output")
	}

	if agentOutput.Review.Body == nil || *agentOutput.Review.Body == "" {
		return fmt.Errorf("'review.body' field is missing or empty")
	}

	if agentOutput.Review.Comments == nil {
		return fmt.Errorf("'review.comments' field is missing")
	}

	validComments := []*github.DraftReviewComment{}
	for _, comment := range agentOutput.Review.Comments {
		if isCommentValid(comment, diffFiles) {
			validComments = append(validComments, comment)
		} else {
			if comment.Path != nil && comment.Line != nil {
				log.Printf("Filtering out invalid comment on file %s at line %d", *comment.Path, *comment.Line)
			} else {
				log.Printf("Filtering out invalid comment with missing path or line")
			}
		}
	}
	agentOutput.Review.Comments = validComments

	log.Println("YAML validation successful.")
	return nil
}

func isCommentValid(comment *github.DraftReviewComment, diffFiles []*gitdiff.File) bool {
	if comment.Path == nil || comment.Line == nil {
		return false // Invalid comment if path or line is missing
	}
	for _, file := range diffFiles {
		if file.NewName == *comment.Path {
			for _, fragment := range file.TextFragments {
				if *comment.Side == "RIGHT" {
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
func getDiffSize(files []*gitdiff.File) string {
	var totalLinesChanged int64
	for _, file := range files {
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

func isGeneratedFile(file *gitdiff.File) bool {
	// Check for "vendor/" path
	if strings.Contains(file.NewName, "vendor/") {
		return true
	}
	if strings.HasSuffix(file.NewName, ".pb.go") ||
		strings.HasSuffix(file.NewName, ".generated.go") ||
		strings.HasSuffix(file.NewName, "zz_generated.deepcopy.go") {
		return true
	}

	// Check for "Code generated by" comment
	for _, fragment := range file.TextFragments {
		for _, line := range fragment.Lines {
			if strings.Contains(line.String(), "Code generated by") {
				return true
			}
			if strings.Contains(line.String(), "DO NOT EDIT") {
				return true
			}
		}
	}

	return false
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

	var filteredFiles []*gitdiff.File
	for _, file := range files {
		if !isGeneratedFile(file) {
			filteredFiles = append(filteredFiles, file)
		}
	}

	return filteredFiles, nil
}

func startCodeServer() (*exec.Cmd, error) {
	log.Println("starting code-server")
	codeServerPath := "/usr/bin/code-server"
	args := []string{"--auth=none", "--bind-addr=0.0.0.0:13337"}
	cmd := exec.Command(codeServerPath, args...)
	cmd.Stdout = os.Stdout
	err := cmd.Start()
	if err != nil {
		return nil, err
	}
	log.Printf("Running code-server in subprocess %d\n", cmd.Process.Pid)
	return cmd, nil
}
