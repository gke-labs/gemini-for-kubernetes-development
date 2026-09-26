package sandbox

import (
	"regexp"
	"strings"
	"testing"
)

// dns1123Label is what a Sandbox name — and so the -lb Service name
// derived from it — has to satisfy. A name that fails this is rejected
// by the API server at create, long after the caller has committed to
// the session id that produced it.
var dns1123Label = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// Creation has to be idempotent. A caller whose create times out
// retries with the same session id; if the name were random it would
// get a second sandbox, and a second engine, for one conversation.
func TestResearchSandboxNameIsDeterministic(t *testing.T) {
	first := ResearchSandboxName("kubernetes", "1d9f5c1e-3f4a-4f0e-9c3b-2a1b7d8e6f00")
	second := ResearchSandboxName("kubernetes", "1d9f5c1e-3f4a-4f0e-9c3b-2a1b7d8e6f00")
	if first != second {
		t.Errorf("same inputs gave %q then %q", first, second)
	}
	if !strings.HasPrefix(first, "rsch-kubernetes-") {
		t.Errorf("name = %q, want an rsch-kubernetes- prefix", first)
	}
}

// Two conversations about one repository must not land in one sandbox:
// the transcript lives on the PVC, so sharing the sandbox shares the
// conversation.
func TestResearchSandboxNameSeparatesSessions(t *testing.T) {
	a := ResearchSandboxName("kubernetes", "session-a")
	b := ResearchSandboxName("kubernetes", "session-b")
	if a == b {
		t.Errorf("distinct sessions collided on %q", a)
	}
}

// The name is assembled from caller-supplied text, so every plausible
// shape of it has to come out as a legal object name within the budget
// the -lb Service's DNS cap allows.
func TestResearchSandboxNameIsAlwaysLegal(t *testing.T) {
	cases := []struct {
		name    string
		repo    string
		session string
	}{
		{"plain", "kubernetes", "s1"},
		{"dots and underscores", "my_repo.v2", "s1"},
		{"uppercase", "MyRepo", "s1"},
		{"leading and trailing junk", "--repo--", "s1"},
		{"very long", strings.Repeat("averylongreponame", 12), "s1"},
		{"unicode", "repo-ünïcødé", "s1"},
		{"empty repo", "", "s1"},
		{"session with slashes", "kubernetes", "org/team/session-1"},
		{"long session", "kubernetes", strings.Repeat("x", 500)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResearchSandboxName(tc.repo, tc.session)
			if len(got) > 60 {
				t.Errorf("name %q is %d chars, over the 60 budget", got, len(got))
			}
			if !dns1123Label.MatchString(got) {
				t.Errorf("name %q is not a legal DNS-1123 label", got)
			}
			if !strings.HasPrefix(got, "rsch-") {
				t.Errorf("name %q lost its prefix", got)
			}
		})
	}
}

// The short id is what the warm pool will eventually mint, and what the
// label carries for selection, so its width is part of the contract
// rather than an implementation detail.
func TestResearchShortIDShape(t *testing.T) {
	got := ResearchShortID("some-session")
	if len(got) != researchShortIDLen {
		t.Errorf("short id %q is %d chars, want %d", got, len(got), researchShortIDLen)
	}
	for _, r := range got {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Errorf("short id %q is not lowercase hex", got)
			break
		}
	}
}

// The sandbox name is a repo slug and a digest, so the object has to
// say for itself which conversation and which repository it belongs to.
// Losing either leaves a running pod nothing can be traced back to.
func TestResearchAnnotationsIdentifyTheSessionAndRepo(t *testing.T) {
	const sessionID = "1d9f5c1e-3f4a-4f0e-9c3b-2a1b7d8e6f00"
	got := researchAnnotations("kubernetes", sessionID,
		"https://github.com/kubernetes/kubernetes.git",
		"https://github.com/kubernetes/kubernetes")

	want := map[string]string{
		"repo":                      "kubernetes",
		"cloneURL":                  "https://github.com/kubernetes/kubernetes.git",
		"htmlURL":                   "https://github.com/kubernetes/kubernetes",
		AnnotationResearchSessionID: sessionID,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("annotation %q = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d annotations, want %d: %+v", len(got), len(want), got)
	}
}

// The label can only carry the digest, so the annotation is the only
// place the full session id survives. A caller's id need not be a legal
// label value, and truncating or sanitising it here would silently make
// the sandbox unaddressable from the session that owns it.
func TestResearchSessionIDAnnotationIsVerbatim(t *testing.T) {
	for _, sessionID := range []string{
		"1d9f5c1e-3f4a-4f0e-9c3b-2a1b7d8e6f00",
		"org/team/session-1",
		"Session With Spaces",
		strings.Repeat("x", 300),
	} {
		got := researchAnnotations("repo", sessionID, "clone", "html")
		if got[AnnotationResearchSessionID] != sessionID {
			t.Errorf("session id %q was stored as %q", sessionID, got[AnnotationResearchSessionID])
		}
	}
}

// The label is what a lookup by session selects on, so it has to be a
// legal label value for every session id a caller might choose, and it
// has to agree with the digest baked into the sandbox name.
func TestResearchLabelsMatchTheSandboxName(t *testing.T) {
	const sessionID = "org/team/session-1"
	labels := researchLabels(sessionID, "barney-s")

	if got := labels[LabelResearchSession]; got != ResearchShortID(sessionID) {
		t.Errorf("session label = %q, want the short id %q", got, ResearchShortID(sessionID))
	}
	if !strings.HasSuffix(ResearchSandboxName("kubernetes", sessionID), labels[LabelResearchSession]) {
		t.Error("the session label does not match the digest in the sandbox name")
	}
	if !dns1123Label.MatchString(labels[LabelResearchSession]) {
		t.Errorf("session label %q is not a legal label value", labels[LabelResearchSession])
	}
	if labels["sandbox.gemini.google.com/type"] != "research" {
		t.Errorf("type label = %q, want research", labels["sandbox.gemini.google.com/type"])
	}
	if labels["factory.gemini.google.com/user"] != "barney-s" {
		t.Errorf("user label = %q, want barney-s", labels["factory.gemini.google.com/user"])
	}
}

// A research sandbox that does not run acpd is a sandbox nothing can
// talk to, and the caller's own --env must survive alongside it.
func TestResearchEnvAddsACPDWithoutDroppingCallerEnv(t *testing.T) {
	caller := []EnvVar{
		{Name: "FOO", Value: "bar"},
		{Name: "GOFLAGS", Value: "-mod=mod"},
	}
	got := researchEnv(caller)

	if len(got) != len(caller)+1 {
		t.Fatalf("got %d vars, want %d", len(got), len(caller)+1)
	}
	for i, want := range caller {
		if got[i] != want {
			t.Errorf("caller env %d = %+v, want %+v", i, got[i], want)
		}
	}
	last := got[len(got)-1]
	if last.Name != EnvACPDEnable || last.Value != "1" {
		t.Errorf("last var = %+v, want %s=1", last, EnvACPDEnable)
	}
}

// Appending must not write through the caller's backing array; a shared
// slice reused for a second sandbox would otherwise accumulate
// duplicates.
func TestResearchEnvDoesNotMutateCallerSlice(t *testing.T) {
	caller := make([]EnvVar, 1, 8) // spare capacity: append would write in place
	caller[0] = EnvVar{Name: "FOO", Value: "bar"}

	researchEnv(caller)

	if len(caller) != 1 || caller[0].Name != "FOO" {
		t.Errorf("caller slice was mutated: %+v", caller)
	}
}

// ACPD_ENABLE goes last so that a caller passing it explicitly cannot
// switch acpd off: the pod spec takes the final value for a repeated
// name.
func TestResearchEnvWinsOverCallerOverride(t *testing.T) {
	got := researchEnv([]EnvVar{{Name: EnvACPDEnable, Value: ""}})
	last := got[len(got)-1]
	if last.Name != EnvACPDEnable || last.Value != "1" {
		t.Errorf("last var = %+v, want %s=1", last, EnvACPDEnable)
	}
}
