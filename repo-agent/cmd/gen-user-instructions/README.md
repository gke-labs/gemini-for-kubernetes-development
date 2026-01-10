# User Instructions Generator

This CLI tool analyzes training data (Agent vs Human reviews) to generate repository-specific user instructions (guidelines) for the Gemini Agent.

## Overview

The tool performs the following steps:
1.  Scans the `training-data/` directory (output of `gen-training-data`).
2.  For each repository (each JSONL file):
    *   Reads the training records (PRs, Agent drafts, Human reviews).
    *   Filters for records with human feedback.
    *   Constructs a prompt for Gemini to analyze discrepancies and identify themes.
    *   Invokes the `gemini` CLI with the prompt.
3.  Writes the generated instructions to `user-instructions/<namespace>/<repo>.json`.

## Usage

```bash
# Ensure you have 'gemini' installed and configured (authenticiated)
which gemini

# Run the generator
go run cmd/gen-user-instructions/main.go \
  --input-dir training-data \
  --output-dir user-instructions \
  --max-records 20
```

## Flags

*   `--input-dir`: Directory containing training data JSONL files (default: `training-data`).
*   `--output-dir`: Directory to write generated user instructions (default: `user-instructions`).
*   `--gemini-cmd`: Path to the `gemini` command (default: `gemini`).
*   `--max-records`: Maximum number of recent records to include in the analysis prompt to fit within context limits (default: 20).

## Output Format

The output is a JSON file compatible with the Gemini Agent's configuration:

```json
{
  "project_name": "owner/repo",
  "guidelines": {
    "clarity": [...],
    "maintainability": [...],
    ...
  }
}
```
