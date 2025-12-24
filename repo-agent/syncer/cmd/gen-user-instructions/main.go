package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const promptTemplate = `
You are an expert software engineering manager and technical lead. 
Your goal is to improve the automated code review agent by analyzing the discrepancies between the Agent's review and the Human's review in the provided training data.

Input: A list of Pull Requests (PRs) for the repository: %s.
For each PR, you have:
- The PR URL.
- The Agent's draft review.
- The Human's actual reviews (which contain the high-value feedback).

Task:
1. Compare the Agent's review with the Human reviews. Identify what the Human caught that the Agent missed, or where the Agent was incorrect/verbose.
2. Identify recurring, high-value feedback themes.
3. Synthesize these themes into specific, actionable instructions (guidelines) for the Agent to improve future reviews.
4. Output the instructions in the specified JSON format.

Output Format (JSON only):
{
  "project_name": "%s",
  "guidelines": {
    "clarity": [
      { "title": "...", "description": "...", "example": "..." }
    ],
    "maintainability": [],
    "code_organization": [],
    "correctness": [],
    "security": [],
    "performance": []
  }
}
Instructions:
- Populated categories only if relevant findings exist.
- Ensure "example" provides a concrete code example or scenario.
- Be concise but specific.

Training Data:
%s
`

// Matches the TrainingDataRecord from gen-training-data
type TrainingDataRecord struct {
	PRNumber     int           `json:"pr_number"`
	Repo         string        `json:"repo"`
	PRURL        string        `json:"pr_url"`
	AgentReview  interface{}   `json:"agent_review_draft"`
	HumanReviews []interface{} `json:"human_reviews"`
	State        string        `json:"pr_state"`
}

func main() {
	inputDir := flag.String("input-dir", "training-data", "Directory containing training data JSONL files")
	outputDir := flag.String("output-dir", "user-instructions", "Directory to write generated user instructions")
	geminiCmd := flag.String("gemini-cmd", "gemini", "Path to the gemini command")
	maxRecords := flag.Int("max-records", 20, "Maximum number of recent records to include in the prompt")
	flag.Parse()

	// Ensure output directory exists
	if err := os.MkdirAll(*outputDir, 0755); err != nil {
		log.Fatalf("Failed to create output directory: %v", err)
	}

	// Walk input directory
	err := filepath.Walk(*inputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(info.Name(), ".jsonl") {
			return nil
		}

		relPath, err := filepath.Rel(*inputDir, path)
		if err != nil {
			return err
		}

		log.Printf("Processing %s", relPath)
		if err := processFile(path, *outputDir, relPath, *geminiCmd, *maxRecords); err != nil {
			log.Printf("Error processing %s: %v", path, err)
		}
		return nil
	})

	if err != nil {
		log.Fatalf("Walk failed: %v", err)
	}
}

func processFile(inputPath, outputDir, relPath, geminiCmd string, maxRecords int) error {
	// Read JSONL
	f, err := os.Open(inputPath)
	if err != nil {
		return err
	}
	defer f.Close()

	var records []TrainingDataRecord
	scanner := bufio.NewScanner(f)
	// Buffer for huge lines
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 10*1024*1024)

	for scanner.Scan() {
		var r TrainingDataRecord
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			log.Printf("Skipping invalid JSON line in %s: %v", inputPath, err)
			continue
		}
		records = append(records, r)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scanner error: %v", err)
	}

	if len(records) == 0 {
		return fmt.Errorf("no records found")
	}

	// Filter records: Keep only those with human reviews
	var usefulRecords []TrainingDataRecord
	for _, r := range records {
		if len(r.HumanReviews) > 0 {
			usefulRecords = append(usefulRecords, r)
		}
	}

	if len(usefulRecords) == 0 {
		log.Printf("No records with human reviews in %s. Skipping.", inputPath)
		return nil
	}

	// Limit records
	if len(usefulRecords) > maxRecords {
		usefulRecords = usefulRecords[len(usefulRecords)-maxRecords:]
	}

	// Extract Repo Name from first record
	repoName := usefulRecords[0].Repo

	// Marshal data for prompt
	dataBytes, err := json.MarshalIndent(usefulRecords, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal error: %v", err)
	}

	prompt := fmt.Sprintf(promptTemplate, repoName, repoName, string(dataBytes))

	// Call Gemini
	cmd := exec.Command(geminiCmd, "-y", "-p", prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	log.Printf("Invoking Gemini for %s...", inputPath)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gemini execution failed: %v. Stderr: %s", err, stderr.String())
	}

	outputJSON := extractJSON(stdout.String())
	if outputJSON == "" {
		return fmt.Errorf("failed to extract JSON from gemini output")
	}

	// Determine output path (mirror input structure)
	// input: training-data/namespace/owner-repo.jsonl
	// output: user-instructions/namespace/owner-repo.json

	// Remove .jsonl extension
	relDir := filepath.Dir(relPath)
	baseName := filepath.Base(relPath)
	fileName := strings.TrimSuffix(baseName, ".jsonl") + ".json"

	outSubDir := filepath.Join(outputDir, relDir)
	if err := os.MkdirAll(outSubDir, 0755); err != nil {
		return fmt.Errorf("failed to create output subdir: %v", err)
	}

	outPath := filepath.Join(outSubDir, fileName)
	if err := os.WriteFile(outPath, []byte(outputJSON), 0644); err != nil {
		return fmt.Errorf("failed to write output: %v", err)
	}

	log.Printf("Generated instructions at %s", outPath)
	return nil
}

func extractJSON(s string) string {
	// Simple extractor for Markdown code blocks or raw JSON
	start := strings.Index(s, "```json")
	if start != -1 {
		s = s[start+7:]
		end := strings.Index(s, "```")
		if end != -1 {
			s = s[:end]
		}
	} else {
		// Try finding first { and last }
		start = strings.Index(s, "{")
		end := strings.LastIndex(s, "}")
		if start != -1 && end != -1 && end > start {
			s = s[start : end+1]
		}
	}
	return strings.TrimSpace(s)
}
