package acpd

import (
	"encoding/json"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/acp"
)

// ModelUsage is a turn's tokens on one model, as the turn_end event
// carries them (TurnEnd.Usage).
type ModelUsage struct {
	Model    string `json:"model"`
	Input    int64  `json:"input"`
	Output   int64  `json:"output"`
	Cached   int64  `json:"cached,omitempty"`
	Thoughts int64  `json:"thoughts,omitempty"`
	Total    int64  `json:"total"`
}

// geminiQuota is the _meta gemini's prompt reply carries its tokens in.
type geminiQuota struct {
	Quota *struct {
		TokenCount *geminiTokens `json:"token_count"`
		ModelUsage []struct {
			Model      string       `json:"model"`
			TokenCount geminiTokens `json:"token_count"`
		} `json:"model_usage"`
	} `json:"quota"`
}

type geminiTokens struct {
	Input  int64 `json:"input_tokens"`
	Output int64 `json:"output_tokens"`
}

// turnUsage is what a prompt reply says the turn used, per model: ACP's
// usage where the engine reports it, else gemini's _meta.quota (per model
// where it says, else the turn's total on model). nil when the reply says
// nothing, as antigravity's do not.
func turnUsage(resp *acp.PromptResponse, model string) []ModelUsage {
	if resp == nil {
		return nil
	}
	if u := resp.Usage; u != nil {
		m := ModelUsage{Model: model, Input: u.InputTokens, Output: u.OutputTokens, Total: u.TotalTokens}
		if u.CachedReadTokens != nil {
			m.Cached = *u.CachedReadTokens
		}
		if u.ThoughtTokens != nil {
			m.Thoughts = *u.ThoughtTokens
		}
		if m.Total == 0 {
			m.Total = m.Input + m.Output
		}
		return []ModelUsage{m}
	}
	var meta geminiQuota
	if len(resp.Meta) == 0 || json.Unmarshal(resp.Meta, &meta) != nil || meta.Quota == nil {
		return nil
	}
	var out []ModelUsage
	for _, mu := range meta.Quota.ModelUsage {
		if mu.TokenCount.Input == 0 && mu.TokenCount.Output == 0 {
			continue
		}
		name := mu.Model
		if name == "" {
			name = model
		}
		out = append(out, ModelUsage{Model: name, Input: mu.TokenCount.Input, Output: mu.TokenCount.Output, Total: mu.TokenCount.Input + mu.TokenCount.Output})
	}
	if len(out) == 0 && meta.Quota.TokenCount != nil {
		tc := meta.Quota.TokenCount
		if tc.Input != 0 || tc.Output != 0 {
			out = append(out, ModelUsage{Model: model, Input: tc.Input, Output: tc.Output, Total: tc.Input + tc.Output})
		}
	}
	return out
}
