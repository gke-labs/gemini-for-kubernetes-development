/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/llm"
)

type streamEvent struct {
	Type       string         `json:"type"`
	Content    string         `json:"content"`
	Role       string         `json:"role"`
	Delta      bool           `json:"delta"`
	Model      string         `json:"model"`
	SessionID  string         `json:"session_id"`
	Stats      *flatStats     `json:"stats"`
	ToolName   string         `json:"tool_name"`
	ToolID     string         `json:"tool_id"`
	Parameters map[string]any `json:"parameters"`
	Status     string         `json:"status"`
}

type flatStats struct {
	TotalTokens  int64 `json:"total_tokens"`
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	Cached       int64 `json:"cached"`
	DurationMs   int64 `json:"duration_ms"`
}

func main() {
	var outputPath string
	flag.StringVar(&outputPath, "output", "", "Path to write the aggregated JSON output")
	flag.Parse()

	reader := bufio.NewReader(os.Stdin)
	var fullResponse strings.Builder
	var lastSessionID string
	var lastModel string
	var finalStats *flatStats

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			fmt.Fprintf(os.Stderr, "Error reading stdin: %v\n", err)
			os.Exit(1)
		}

		var ev streamEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			// Skip non-json lines
			continue
		}

		switch ev.Type {
		case "init":
			lastSessionID = ev.SessionID
			lastModel = ev.Model
		case "message":
			if ev.Role == "assistant" && ev.Delta {
				fmt.Print(ev.Content)
				fullResponse.WriteString(ev.Content)
			}
		case "tool_use":
			fmt.Printf("\n[tool_use] %s\n", ev.ToolName)
		case "tool_result":
			fmt.Printf("[tool_result] %s: %s\n", ev.ToolID, ev.Status)
		case "result":
			finalStats = ev.Stats
		case "error":
			fmt.Fprintf(os.Stderr, "\nError from gemini: %s\n", ev.Content)
		}
	}

	// Final newline for stdout
	fmt.Println()

	if outputPath != "" {
		// Reconstruct the old format
		output := llm.GeminiJSONOutput{
			SessionID: lastSessionID,
			Response:  fullResponse.String(),
		}

		if finalStats != nil {
			model := lastModel
			if model == "" {
				model = "gemini-cli"
			}
			
			// We need to use the internal structure that GeminiStatsJSON expects
			stats := llm.GeminiStatsJSON{
				Models: make(map[string]struct {
					API struct {
						TotalRequests  int64 `json:"totalRequests"`
						TotalErrors    int64 `json:"totalErrors"`
						TotalLatencyMs int64 `json:"totalLatencyMs"`
					} `json:"api"`
					Tokens struct {
						Input      int64 `json:"input"`
						Prompt     int64 `json:"prompt"`
						Candidates int64 `json:"candidates"`
						Total      int64 `json:"total"`
						Cached     int64 `json:"cached"`
						Thoughts   int64 `json:"thoughts"`
						Tool       int64 `json:"tool"`
					} `json:"tokens"`
				}),
			}

			mStats := stats.Models[model]
			mStats.API.TotalRequests = 1
			mStats.API.TotalLatencyMs = finalStats.DurationMs
			mStats.Tokens.Input = finalStats.InputTokens
			mStats.Tokens.Candidates = finalStats.OutputTokens
			mStats.Tokens.Total = finalStats.TotalTokens
			mStats.Tokens.Cached = finalStats.Cached
			
			stats.Models[model] = mStats
			output.Stats = stats
		}

		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error marshaling output: %v\n", err)
			os.Exit(1)
		}

		if err := os.WriteFile(outputPath, data, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing output file: %v\n", err)
			os.Exit(1)
		}
	}
}
