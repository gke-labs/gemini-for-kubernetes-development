package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func reviewTranscript() string {
	return `{"seq":1,"time":"t1","kind":"user_prompt","data":{"text":"Review it"}}
{"seq":2,"time":"t2","kind":"agent_message_chunk","data":{"content":{"type":"text","text":"Looking"}}}
not json
{"seq":3,"time":"t3","kind":"tool_call","data":{"title":"go test ./..."}}
`
}

func streamsServer(t *testing.T) *fakeSessions {
	t.Helper()
	return &fakeSessions{files: map[string]string{
		planTask + "/session/stream.ndjson":        `{"seq":1}`,
		planTask + "/session-review/stream.ndjson": reviewTranscript(),
		planTask + "/steps/01-1-setup-git.log":     "ok",
		planTask + "/execution.log":                "::step 8:ask (ask)\nwork\n::step unchanged (run)\n::step 10:ask (ask)\n",
	}}
}

// A task lists its side sessions - session-<name>/ with a transcript,
// never its own session/ - and the step it is at.
func TestATaskSessionListsItsSideSessionsAndStep(t *testing.T) {
	r := taskSessionTestServer(t, streamsServer(t), true, []*unstructured.Unstructured{issueSandboxCR("alice", 1)},
		researchPod("alice", issueSandbox, "10.1.2.3", corev1.PodRunning))
	w := doJSON(t, r, http.MethodGet, taskSessionAt+"/streams", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var got TaskSessionStreams
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Streams) != 1 || got.Streams[0] != "review" || got.Step != "10:ask (ask)" {
		t.Errorf("got %+v", got)
	}
}

// A side session's events come from its transcript, after the seq the
// client has; lines that do not parse are skipped.
func TestATaskSideSessionServesItsEventsAfterASeq(t *testing.T) {
	r := taskSessionTestServer(t, streamsServer(t), true, []*unstructured.Unstructured{issueSandboxCR("alice", 1)},
		researchPod("alice", issueSandbox, "10.1.2.3", corev1.PodRunning))
	read := func(path string) (events []map[string]any, more bool, code int) {
		w := doJSON(t, r, http.MethodGet, path, "")
		var body struct {
			Events []map[string]any `json:"events"`
			More   bool             `json:"more"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return body.Events, body.More, w.Code
	}
	all, more, code := read(taskSessionAt + "/streams/review")
	if code != http.StatusOK || len(all) != 3 || more || all[0]["kind"] != "user_prompt" {
		t.Fatalf("all: %d events, more %v, status %d: %v", len(all), more, code, all)
	}
	rest, _, _ := read(taskSessionAt + "/streams/review?after=2")
	if len(rest) != 1 || rest[0]["kind"] != "tool_call" {
		t.Errorf("after=2: %v", rest)
	}
	if _, _, code := read(taskSessionAt + "/streams/nope"); code != http.StatusNotFound {
		t.Errorf("a missing session: status %d", code)
	}
	for _, bad := range []string{"/streams/..%2Fsession", "/streams/Review", "/streams/review?after=x"} {
		if _, _, code := read(taskSessionAt + bad); code != http.StatusBadRequest && code != http.StatusNotFound {
			t.Errorf("%s: status %d", bad, code)
		}
	}
}

func TestEventsAfterBoundsTheAnswer(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 5; i++ {
		b.WriteString(`{"seq":` + string(rune('0'+i)) + `}` + "\n")
	}
	got, more := eventsAfter([]byte(b.String()), 1, 2)
	if len(got) != 2 || !more || string(got[0]) != `{"seq":2}` {
		t.Errorf("got %s, more %v", got, more)
	}
}

func TestSideSessionsAndLastStep(t *testing.T) {
	got := sideSessions([]string{"session/stream.ndjson", "session-review/stream.ndjson", "session-review/session.json", "session-X/stream.ndjson", "steps/x.log", "session-a/stream.ndjson"})
	if strings.Join(got, ",") != "a,review" {
		t.Errorf("sideSessions = %v", got)
	}
	if s := lastStep([]byte("noise\n::step 3:run (run)\n")); s != "3:run (run)" {
		t.Errorf("lastStep = %q", s)
	}
	if s := lastStep(nil); s != "" {
		t.Errorf("lastStep(nil) = %q", s)
	}
}
