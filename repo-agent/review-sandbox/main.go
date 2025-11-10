package main

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/bluekeyes/go-gitdiff/gitdiff"
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
	var agentFn func(string) ([]byte, error)
	switch agentName {
	case "gemini-cli":
		log.Println("Setting up Gemini CLI")
		if err := setupGemini(); err != nil {
			return err
		}
		agentFn = runGeminiCli
	default:
		return fmt.Errorf("unknown agent name: %s", agentName)
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

		output, err := agentFn(currentPrompt)
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

func setupGemini() error {
	// if .gemini directory exists in /workspaces copy it to home directory
	if _, err := os.Stat("/workspaces/.gemini"); err == nil {
		log.Println(".gemini directory exists in /workspaces, copying to repo directory")
		// if desitation .gemini directory exists move it to .gemini.bak
		if _, err := os.Stat(".gemini"); err == nil {
			log.Println(".gemini directory exists in repo directory, moving to .gemini.bak")
			err := os.Rename(".gemini", ".gemini.bak")
			if err != nil {
				return fmt.Errorf("failed to move .gemini to .gemini.bak: %v", err)
			}
		}
		cmd := exec.Command("cp", "-R", "/workspaces/.gemini", ".gemini")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to copy .gemini directory: %v", err)
		}
	} else {
		log.Println(".gemini directory does not exist in /workspaces")
	}

	geminiKey, err := os.ReadFile("/tokens/gemini")
	if err != nil {
		return fmt.Errorf("failed to read /tokens/gemini: %v", err)
	}
	os.Setenv("GEMINI_API_KEY", string(geminiKey))
	return nil
}

func runGeminiCli(agentPrompt string) ([]byte, error) {
	log.Println("running gemini")

	var stdout, stderr bytes.Buffer
	log.Printf("running gemini with prompt: %s", agentPrompt)
	cmd := exec.Command("gemini", "-y", "-p", agentPrompt)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		log.Printf("gemini command failed: %v. Stderr: %s", err, stderr.String())
		return nil, err
	}

	return stripYAMLMarkers(stdout.Bytes()), nil
}

// stripYAMLMarkers looks for ```yaml and ``` markers in the input byte slice.
// If found, it strips these markers and returns the content between them.
// If markers are not found, the original byte slice is returned.
func stripYAMLMarkers(input []byte) []byte {
	startMarker := []byte("```yaml")
	endMarker := []byte("```")

	startIndex := bytes.Index(input, startMarker)
	if startIndex == -1 {
		return input // Start marker not found
	}

	// Adjust startIndex to point after the start marker
	startIndex += len(startMarker)

	endIndex := bytes.Index(input[startIndex:], endMarker)
	if endIndex == -1 {
		return input // End marker not found after start marker
	}

	// Adjust endIndex to be relative to the original input slice
	endIndex += startIndex

	// Extract the content between the markers, trimming any leading/trailing whitespace
	return bytes.TrimSpace(input[startIndex:endIndex])
}
