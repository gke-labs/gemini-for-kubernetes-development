package acpd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Handed to a terminal, a session's marker says so and counts the turns
// the transcript had; handed back, it says view, and a create is no
// longer refused.
func TestReleaseAndReturn(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	dir := filepath.Join(s.stateDir, "r1")
	if _, err := s.Release("r1", false); err != ErrNoConversation {
		t.Fatalf("release without a record: %v", err)
	}
	if err := writeRecord(dirMust(t, dir), Record{ACPSessionID: "abc-123", Engine: "gemini", CWD: "/workspaces/repo"}); err != nil {
		t.Fatal(err)
	}
	tr, err := OpenTranscript(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = tr.AppendValue(KindUserPrompt, map[string]string{"text": "one"})
	_ = tr.AppendValue(KindUserPrompt, map[string]string{"text": "two"})
	_ = tr.Close()

	rec, err := s.Release("r1", false)
	if err != nil || rec.ACPSessionID != "abc-123" {
		t.Fatalf("release: %+v, %v", rec, err)
	}
	h, ok := ReadHandoff(dir)
	if !ok || h.To != "terminal" || h.UserTurns != 2 {
		t.Fatalf("marker %+v", h)
	}
	if !s.inTerminal("r1", dir) {
		t.Error("not in a terminal after release")
	}
	s.SetHandoffTerminal(func(string) bool { return false })
	if s.inTerminal("r1", dir) {
		t.Error("in a terminal that has ended")
	}
	if err := s.Return("r1", false); err != nil {
		t.Fatal(err)
	}
	if h, _ := ReadHandoff(dir); h.To != "view" || h.UserTurns != 2 {
		t.Errorf("after return: %+v", h)
	}
}

// A load back from a terminal writes only the replayed turns past those
// the transcript had, as turns of their own.
func TestReplayKeepsTheTerminalsTurns(t *testing.T) {
	dir := t.TempDir()
	tr, err := OpenTranscript(dir)
	if err != nil {
		t.Fatal(err)
	}
	k := &replayKeep{skip: 1}
	upd := func(kind, text string) {
		data, _ := json.Marshal(map[string]any{"sessionUpdate": kind, "content": map[string]string{"type": "text", "text": text}})
		k.update(tr, kind, data)
	}
	upd("user_message_chunk", "one")
	upd("agent_message_chunk", "answer one")
	upd("user_message_chunk", "two from ")
	upd("user_message_chunk", "the terminal")
	upd("agent_message_chunk", "answer two")
	k.end(tr)
	_ = tr.Close()
	data, _ := os.ReadFile(filepath.Join(dir, "stream.ndjson"))
	got := string(data)
	for _, want := range []string{`"kind":"user_prompt"`, `two from the terminal`, `answer two`, `"kind":"turn_end"`} {
		if !strings.Contains(got, want) {
			t.Errorf("transcript lacks %s:\n%s", want, got)
		}
	}
	if strings.Contains(got, "answer one") || strings.Contains(got, `"one"`) {
		t.Errorf("transcript repeats a turn it had:\n%s", got)
	}
}

func dirMust(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}
