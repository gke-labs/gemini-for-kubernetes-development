package sandbox

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// dns1123Label is what a Sandbox name — and so the -lb Service name
// derived from it — has to satisfy. A name that fails this is rejected
// by the API server at create, long after the caller has committed to
// the session id that produced it.
var dns1123Label = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// Creation has to be idempotent. A caller whose create times out
// retries with the same key; if the name were random it would get a
// second sandbox, and a second engine, for one conversation.
func TestScopedSandboxNameIsDeterministic(t *testing.T) {
	first := ScopedSandboxName("rsch", "kubernetes", "1d9f5c1e-3f4a-4f0e-9c3b-2a1b7d8e6f00")
	second := ScopedSandboxName("rsch", "kubernetes", "1d9f5c1e-3f4a-4f0e-9c3b-2a1b7d8e6f00")
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
func TestScopedSandboxNameSeparatesKeys(t *testing.T) {
	a := ScopedSandboxName("rsch", "kubernetes", "session-a")
	b := ScopedSandboxName("rsch", "kubernetes", "session-b")
	if a == b {
		t.Errorf("distinct keys collided on %q", a)
	}
	if got := ScopedSandboxName("rsch", "kubernetes", ""); got != "rsch-kubernetes" {
		t.Errorf("a repo-scoped name = %q, want rsch-kubernetes", got)
	}
}

// The name is assembled from caller-supplied text, so every plausible
// shape of it has to come out as a legal object name within the budget
// the -lb Service's DNS cap allows.
func TestScopedSandboxNameIsAlwaysLegal(t *testing.T) {
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
		{"repo scope, very long", strings.Repeat("averylongreponame", 12), ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ScopedSandboxName("rsch", tc.repo, tc.session)
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

// The short id is what the key label carries for selection, so its width
// is part of the contract rather than an implementation detail.
func TestShortIDShape(t *testing.T) {
	got := ShortID("some-session")
	if len(got) != shortIDLen {
		t.Errorf("short id %q is %d chars, want %d", got, len(got), shortIDLen)
	}
	for _, r := range got {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Errorf("short id %q is not lowercase hex", got)
			break
		}
	}
}

// The sandbox name is a repo slug and a digest, so the object has to
// say for itself which key and which repository it belongs to; the key
// label is the digest in the name, the annotation the key verbatim, which
// no label could hold.
func TestScopedLabelsAndAnnotations(t *testing.T) {
	const key = "org/team/Session With Spaces"
	s := Scoped{Prefix: "rsch", Scope: "key", Key: key}
	labels := s.labels("owner/kubernetes", "barney-s")
	want := map[string]string{
		LabelType:                           "rsch",
		LabelScope:                          "key",
		LabelKey:                            ShortID(key),
		LabelRepo:                           labelValue("owner/kubernetes"),
		"factory.gemini.google.com/managed": "true",
		"factory.gemini.google.com/user":    "barney-s",
	}
	for k, v := range want {
		if labels[k] != v {
			t.Errorf("label %q = %q, want %q", k, labels[k], v)
		}
	}
	if !strings.HasSuffix(ScopedSandboxName("rsch", "kubernetes", key), labels[LabelKey]) {
		t.Error("the key label does not match the digest in the sandbox name")
	}
	annotations := s.annotations("kubernetes", "clone", "html")
	if annotations[AnnotationKeyID] != key || annotations["repo"] != "kubernetes" || annotations["cloneURL"] != "clone" || annotations["htmlURL"] != "html" {
		t.Errorf("annotations = %v", annotations)
	}

	repo := Scoped{Prefix: "tools", Scope: "repo"}
	if _, ok := repo.labels("kubernetes", "u")[LabelKey]; ok {
		t.Error("a repo-scoped sandbox has a key label")
	}
	if _, ok := repo.annotations("kubernetes", "", "")[AnnotationKeyID]; ok {
		t.Error("a repo-scoped sandbox has a key-id annotation")
	}
}

// scopedSB is a sandbox of a given age, with or without the receipt
// that says its setup finished.
func scopedSB(age time.Duration, ready bool) *unstructured.Unstructured {
	meta := map[string]interface{}{
		"name":              "rsch-repo-deadbeef",
		"namespace":         "alice",
		"creationTimestamp": time.Now().Add(-age).UTC().Format(time.RFC3339),
	}
	if ready {
		meta["annotations"] = map[string]interface{}{
			AnnotationReady: time.Now().UTC().Format(time.RFC3339),
		}
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1alpha1",
		"kind":       "Sandbox",
		"metadata":   meta,
	}}
}

// The receipt is the only protection from replacement, and it is
// absolute: a conversation someone comes back to after a week is not a
// husk, and deleting it would take the transcript with it.
func TestFinishedScopedSandboxIsNeverReplaced(t *testing.T) {
	old := scopedSB(30*24*time.Hour, true)
	if got := scopedDisposition(old, time.Now()); got != adopt {
		t.Errorf("disposition of a ready month-old sandbox = %v, want adopt", got)
	}
}

// An unfinished sandbox from a launch that is still running, or was
// killed moments ago, is finished by the relaunch rather than rebuilt.
// Replacing on every relaunch would tear down and remake a pod that
// each new controller was about to complete — and repeated restarts are
// exactly when interrupted launches happen, so that policy would never
// converge.
func TestYoungUnfinishedScopedSandboxIsAdopted(t *testing.T) {
	young := scopedSB(setupGrace/2, false)
	if got := scopedDisposition(young, time.Now()); got != adopt {
		t.Errorf("disposition of a young unfinished sandbox = %v, want adopt", got)
	}
}

// Past the grace there is no launch left to finish it: a launch cannot
// outlive its own timeout. What remains is a pod with an empty
// workspace, which an engine cannot chdir into — so it is replaced
// rather than handed to the session as if it worked.
func TestOldUnfinishedScopedSandboxIsReplaced(t *testing.T) {
	stale := scopedSB(setupGrace+time.Minute, false)
	if got := scopedDisposition(stale, time.Now()); got != replace {
		t.Errorf("disposition of a stale unfinished sandbox = %v, want replace", got)
	}
}

// An empty annotation value is not a receipt. The receipt is written as
// a timestamp, and a blank one would mean a patch that half-landed.
func TestBlankReadyAnnotationIsNotAReceipt(t *testing.T) {
	sb := scopedSB(time.Hour, false)
	sb.SetAnnotations(map[string]string{AnnotationReady: ""})
	if sandboxReady(sb) {
		t.Error("a blank ready annotation was read as a finished setup")
	}
}
