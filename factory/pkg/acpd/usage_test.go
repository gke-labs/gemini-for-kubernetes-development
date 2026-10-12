package acpd

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/acp"
)

func i64(n int64) *int64 { return &n }

// What a prompt reply says the turn used: gemini's _meta.quota (per model
// where it says, else the turn's total on the session's model), ACP's
// usage field, or nothing (antigravity).
func TestTurnUsage(t *testing.T) {
	for name, tc := range map[string]struct {
		resp *acp.PromptResponse
		want []ModelUsage
	}{
		"gemini, per model": {
			resp: &acp.PromptResponse{StopReason: "end_turn", Meta: json.RawMessage(`{"quota":{"token_count":{"input_tokens":30,"output_tokens":5},
				"model_usage":[{"model":"gemini-3-pro","token_count":{"input_tokens":20,"output_tokens":4}},{"model":"gemini-3-flash","token_count":{"input_tokens":10,"output_tokens":1}}]}}`)},
			want: []ModelUsage{{Model: "gemini-3-pro", Input: 20, Output: 4, Total: 24}, {Model: "gemini-3-flash", Input: 10, Output: 1, Total: 11}},
		},
		"gemini, the turn's total only": {
			resp: &acp.PromptResponse{StopReason: "end_turn", Meta: json.RawMessage(`{"quota":{"token_count":{"input_tokens":30,"output_tokens":5},"model_usage":[]}}`)},
			want: []ModelUsage{{Model: "m", Input: 30, Output: 5, Total: 35}},
		},
		"gemini, a command (no tokens)": {
			resp: &acp.PromptResponse{StopReason: "end_turn", Meta: json.RawMessage(`{"quota":{"token_count":{"input_tokens":0,"output_tokens":0},"model_usage":[]}}`)},
		},
		"ACP usage": {
			resp: &acp.PromptResponse{StopReason: "end_turn", Usage: &acp.Usage{InputTokens: 7, OutputTokens: 3, TotalTokens: 12, CachedReadTokens: i64(2), ThoughtTokens: i64(2)}},
			want: []ModelUsage{{Model: "m", Input: 7, Output: 3, Cached: 2, Thoughts: 2, Total: 12}},
		},
		"nothing (antigravity)": {resp: &acp.PromptResponse{StopReason: "end_turn"}},
	} {
		if got := turnUsage(tc.resp, "m"); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: turnUsage = %+v, want %+v", name, got, tc.want)
		}
	}
}
