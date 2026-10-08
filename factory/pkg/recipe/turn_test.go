package recipe

import (
	"encoding/json"
	"testing"
)

func TestTurnReply(t *testing.T) {
	message := func(text string) string {
		b, _ := json.Marshal(map[string]any{"kind": "agent_message_chunk", "data": map[string]any{
			"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}}})
		return string(b)
	}
	thought := `{"kind":"agent_thought_chunk","data":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"hmm"}}}`
	toolCall := `{"kind":"tool_call","data":{"sessionUpdate":"tool_call","toolCallId":"t","title":"ls"}}`
	end := `{"kind":"turn_end","data":{"stopReason":"end_turn"}}`

	tests := []struct {
		name   string
		events []string
		want   string
	}{
		{name: "a reply", events: []string{thought, message("review:\n"), message("  body: ok"), end}, want: "review:\n  body: ok"},
		{name: "narration before a tool call is dropped", events: []string{message("let me look"), toolCall, message("the answer"), end}, want: "the answer"},
		// gemini retried the call after a 503 halfway through the answer:
		// the half-sent first answer, a thought, then the whole answer again.
		{name: "a restarted generation keeps only the last", events: []string{
			thought, message("review:\n  body: |\n    The change enfo"), thought, message("review:\n  body: |\n    The change consistently"), end,
		}, want: "review:\n  body: |\n    The change consistently"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var tr turn
			for i, line := range tc.events {
				reply, done, err := tr.event([]byte(line))
				if done != (i == len(tc.events)-1) {
					t.Fatalf("event %d: done %v", i, done)
				}
				if done {
					if err != nil || reply != tc.want {
						t.Fatalf("reply %q, %v; want %q", reply, err, tc.want)
					}
				}
			}
		})
	}
}
