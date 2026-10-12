package recipe

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/acpd"
)

// UsageFile is the task-directory file a task's token usage is in, as
// lib.sh's record_*_usage write it and usagereport harvests it.
const UsageFile = "token-usage.json"

// usageFile is UsageFile's shape: per model, its requests and tokens.
type usageFile struct {
	Models map[string]*modelUsage `json:"models"`
}

type modelUsage struct {
	API struct {
		TotalRequests  int64 `json:"totalRequests"`
		TotalErrors    int64 `json:"totalErrors"`
		TotalLatencyMs int64 `json:"totalLatencyMs"`
	} `json:"api"`
	Tokens struct {
		Input    int64 `json:"input"`
		Output   int64 `json:"output"`
		Total    int64 `json:"total"`
		Cached   int64 `json:"cached"`
		Thoughts int64 `json:"thoughts"`
	} `json:"tokens"`
}

// AddUsage adds a turn's usage to taskDir's UsageFile, a request per
// model. Nothing to add (an engine that reports none) writes nothing.
func AddUsage(taskDir string, usage []acpd.ModelUsage) error {
	if len(usage) == 0 {
		return nil
	}
	path := filepath.Join(taskDir, UsageFile)
	f := usageFile{Models: map[string]*modelUsage{}}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &f)
		if f.Models == nil {
			f.Models = map[string]*modelUsage{}
		}
	}
	for _, u := range usage {
		m := f.Models[u.Model]
		if m == nil {
			m = &modelUsage{}
			f.Models[u.Model] = m
		}
		m.API.TotalRequests++
		m.Tokens.Input += u.Input
		m.Tokens.Output += u.Output
		m.Tokens.Total += u.Total
		m.Tokens.Cached += u.Cached
		m.Tokens.Thoughts += u.Thoughts
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
