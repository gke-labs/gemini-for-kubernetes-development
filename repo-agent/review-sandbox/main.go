package main

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"time"

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

	var agentFn func() ([]byte, error)
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

	var output []byte
	var err error
	maxRetries := 3
	for i := 0; i < maxRetries; i++ {
		log.Printf("Running Agent %s (attempt %d/%d)", agentName, i+1, maxRetries)
		output, err = agentFn()
		if err != nil {
			log.Printf("Agent run failed: %v. Retrying...", err)
			time.Sleep(30 * time.Second)
			continue
		}

		// Write output to file for debugging, regardless of validation result.
		filename := fmt.Sprintf("../agent-output-run%d.txt", i+1)
		if err := os.WriteFile(filename, output, 0644); err != nil {
			log.Printf("Failed to write agent output to %s: %v", filename, err)
		} else {
			log.Printf("Wrote agent output to %s", filename)
		}

		if err = validateAgentOutput(output); err == nil {
			log.Println("Agent output validation successful.")
			// Write output to file for debugging, regardless of validation result.
			filename := "../agent-output.txt"
			if err := os.WriteFile(filename, output, 0644); err != nil {
				log.Printf("Failed to write agent output to %s: %v", filename, err)
				continue
			}
			log.Printf("Wrote agent output to %s", filename)
			return nil // Success
		}

		log.Printf("Agent output validation failed: %v. Retrying...", err)
		time.Sleep(5 * time.Second)
	}

	return fmt.Errorf("agent output failed to validate after %d retries", maxRetries)
}

func validateAgentOutput(output []byte) error {
	var agentOutput AgentOutput
	if err := yaml.Unmarshal(output, &agentOutput); err != nil {
		return fmt.Errorf("failed to unmarshal yaml: %v", err)
	}

	if agentOutput.Review == nil {
		return fmt.Errorf("'review' field is missing from yaml output")
	}

	if agentOutput.Review.Body == nil || *agentOutput.Review.Body == "" {
		return fmt.Errorf("'review.body' field is missing or empty")
	}

	log.Println("YAML validation successful.")
	return nil
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

func runGeminiCli() ([]byte, error) {
	log.Println("running gemini")
	agentPrompt := os.Getenv("AGENT_PROMPT")

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
