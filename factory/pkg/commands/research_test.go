package commands

import (
	"encoding/json"
	"strings"
	"testing"
)

// The marker line is the whole machine-readable contract of `factory
// research start`. A caller finds it by prefix and parses the rest as
// JSON, so the prefix has to be unambiguous and the remainder has to be
// exactly one JSON object on one line.
func TestResearchReadyMarkerLineParses(t *testing.T) {
	info := ResearchSandboxInfo{
		Sandbox:   "rsch-kubernetes-1a2b3c4d",
		Namespace: "barney-s",
		SessionID: "1d9f5c1e-3f4a-4f0e-9c3b-2a1b7d8e6f00",
		Repo:      "kubernetes",
		PodIP:     "10.52.3.14",
		Port:      researchACPDPort,
		CWD:       "/workspaces/kubernetes",
	}
	encoded, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	line := ResearchReadyMarker + string(encoded)

	if strings.Count(line, "\n") != 0 {
		t.Error("the marker line must be a single line")
	}
	_, rest, found := strings.Cut(line, ResearchReadyMarker)
	if !found {
		t.Fatalf("marker not found in %q", line)
	}

	var decoded ResearchSandboxInfo
	if err := json.Unmarshal([]byte(rest), &decoded); err != nil {
		t.Fatalf("decoding the marker payload: %v", err)
	}
	if decoded != info {
		t.Errorf("round trip changed the payload:\n got %+v\nwant %+v", decoded, info)
	}
}

// A caller scanning progress output must not mistake a narration line
// for the result. The marker ends with a space so that a bare mention
// of the constant's stem does not match.
func TestResearchReadyMarkerIsDistinct(t *testing.T) {
	progress := []string{
		"Ensuring research sandbox for foo/bar (session s1)...",
		"Connecting to sandbox rsch-bar-1a2b3c4d via envd...",
		"Preparing checkout of bar...",
	}
	for _, line := range progress {
		if strings.HasPrefix(line, ResearchReadyMarker) {
			t.Errorf("progress line matched the marker: %q", line)
		}
	}
	if !strings.HasSuffix(ResearchReadyMarker, " ") {
		t.Error("marker should end with a separator so the JSON starts cleanly")
	}
}

// owner and repo are taken from a user-supplied URL and end up in a
// filesystem path and a clone URL. Anything that could change the
// meaning of either has to be refused up front.
func TestValidRepoPart(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"kubernetes", true},
		{"repo-agent", true},
		{"repo_agent", true},
		{"repo.v2", true},
		{"Repo123", true},
		{"", false},
		{".", false},
		{"..", false},
		{"repo;rm -rf /", false},
		{"repo name", false},
		{"repo$(id)", false},
		{"repo`id`", false},
		{"repo/../etc", false},
		{"repo\nname", false},
		{"repo'quote", false},
		{`repo"quote`, false},
		{"repo&&touch", false},
		{"repo|tee", false},
		{"repo\x00null", false},
	}
	for _, tc := range cases {
		if got := validRepoPart(tc.in); got != tc.want {
			t.Errorf("validRepoPart(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// The port reported to the caller must be the one acpd actually binds.
// They are separate constants in separate packages, so nothing but a
// test stops them drifting apart.
func TestResearchPortMatchesACPDDefault(t *testing.T) {
	if researchACPDPort != 49984 {
		t.Errorf("researchACPDPort = %d, want acpd's default 49984", researchACPDPort)
	}
}
