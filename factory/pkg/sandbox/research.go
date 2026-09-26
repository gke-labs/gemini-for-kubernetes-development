package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
)

// LabelResearchSession carries the session a research sandbox belongs
// to, so the sandbox can be found from the session id alone. It holds
// the short form, because a label value has a charset and a length the
// caller's session id need not respect.
const LabelResearchSession = "sandbox.gemini.google.com/research-session"

// Annotations a research sandbox carries. Its name is a repo slug and a
// digest, which on its own says nothing about what the sandbox is for;
// these are what `kubectl describe` and the board read it back from.
//
// Prefixed, as the runbook annotations are, because they are specific
// to this sandbox type. The repository is recorded in the unprefixed
// repo/cloneURL/htmlURL keys instead — every sandbox type writes those
// and the UI already reads them, so a research-specific spelling of the
// same three values would only be a second place to keep in sync.
const (
	// AnnotationResearchSessionID is the caller's session id in full —
	// the one that addresses the conversation in acpd. The label cannot
	// hold it, so this is the only place the mapping from sandbox back
	// to session survives.
	AnnotationResearchSessionID = "sandbox.gemini.google.com/research-session-id"
)

// researchLabels is the label set for one research session's sandbox.
//
// Type "research" keeps these out of the board's task-slot accounting,
// as runbook sandboxes are.
func researchLabels(sessionID, user string) map[string]string {
	return map[string]string{
		"sandbox.gemini.google.com/type":    "research",
		"factory.gemini.google.com/managed": "true",
		"factory.gemini.google.com/user":    user,
		LabelResearchSession:                ResearchShortID(sessionID),
	}
}

// researchAnnotations records what the sandbox is for: which
// conversation, and which repository.
func researchAnnotations(repoName, sessionID, cloneURL, htmlURL string) map[string]string {
	return map[string]string{
		"repo":                      repoName,
		"cloneURL":                  cloneURL,
		"htmlURL":                   htmlURL,
		AnnotationResearchSessionID: sessionID,
	}
}

// researchShortIDLen is how much of the session digest goes in the name.
// Eight hex characters is 32 bits: enough that a collision between two
// live sessions on one repo is not a practical concern, short enough to
// leave the repo slug readable in kubectl output.
const researchShortIDLen = 8

// ResearchShortID is the session's short form, derived from its id.
//
// A research sandbox is per session, not per repo, so the name needs
// something unique in it. Deriving that from the session id rather than
// minting it randomly makes creation idempotent: a caller that retries
// after a timeout computes the same name and finds the sandbox it
// already made, instead of starting a second engine for one
// conversation.
//
// The eventual warm pool inverts this — the pool mints the short id at
// fill time and the session adopts it, because a Sandbox name cannot be
// changed on claim. Until the pool exists, deriving is the direction
// that keeps retries safe.
func ResearchShortID(sessionID string) string {
	sum := sha256.Sum256([]byte(sessionID))
	return hex.EncodeToString(sum[:])[:researchShortIDLen]
}

// ResearchSandboxName returns the sandbox name for one research
// session: rsch-<slug>-<short id>, slug-budgeted for the companion
// -lb Service's DNS cap.
func ResearchSandboxName(repo, sessionID string) string {
	short := ResearchShortID(sessionID)
	slug := slugifyName(repo)
	if budget := 60 - len("rsch-") - len(short) - 1; len(slug) > budget {
		slug = strings.Trim(slug[:budget], "-")
	}
	return "rsch-" + slug + "-" + short
}

// slugifyName reduces a name to the lowercase alphanumeric-and-dash
// form a Kubernetes object name allows.
func slugifyName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// EnvACPDEnable gates the listener started by `factory daemon`.
//
// Opt-in rather than always-on: acpd is an unauthenticated port that
// starts agent processes, and only research sandboxes have any use for
// it. Every other sandbox keeps the surface it has today.
//
// It lives here, next to the code that writes it into a pod spec,
// because the writer and the reader must not be able to drift: pkg/commands
// imports this package to start the daemon, so both sides share one
// definition.
const EnvACPDEnable = "ACPD_ENABLE"

// researchEnv adds ACPD_ENABLE to the caller's environment.
//
// Appended rather than assigned, so a caller's --env still reaches the
// sandbox. Appended last because the pod spec takes the final value for
// a repeated name: a caller cannot switch acpd off by accident, and a
// research sandbox without acpd is a sandbox nothing can talk to.
func researchEnv(envs []EnvVar) []EnvVar {
	out := make([]EnvVar, 0, len(envs)+1)
	out = append(out, envs...)
	return append(out, EnvVar{Name: EnvACPDEnable, Value: "1"})
}

// EnsureResearchSandbox ensures the sandbox for one research session.
//
// Unlike the explore sandbox, which is one per repo and long-lived,
// this is one per conversation: the transcript lives on the PVC and
// dies with the sandbox, so sharing one between sessions would mean
// sharing a transcript.
//
// The sandbox runs acpd because ACPD_ENABLE is set here. Nothing else
// turns it on, so every other sandbox type is unaffected.
func EnsureResearchSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, repoName, sessionID, cloneURL, htmlURL, image, diskSize, ephemeralStorage string, secrets []SecretMount, envs []EnvVar, user string) (string, error) {
	if sessionID == "" {
		return "", fmt.Errorf("research sandbox needs a session id")
	}
	name := ResearchSandboxName(repoName, sessionID)
	if err := ensureDeployerServiceAccount(ctx, kubeClient, namespace); err != nil {
		return "", err
	}

	sb, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	switch {
	case err == nil && terminating(sb):
		// Reusing a sandbox that is being deleted means waiting for a
		// pod that will never be ready. Wait for the name to free up
		// and fall through to creating a fresh one.
		if werr := awaitSandboxGone(ctx, kubeClient, namespace, name); werr != nil {
			return "", werr
		}
	case err == nil:
		ensureSandboxUserLabel(ctx, kubeClient, namespace, sb, user)
		return name, nil
	case !strings.Contains(err.Error(), "not found"):
		return "", fmt.Errorf("checking sandbox existence: %w", err)
	}

	if diskSize == "" {
		diskSize = "10Gi"
	}

	opt := AgentSandboxOptions{
		DevSandboxOptions: DevSandboxOptions{
			Name:               name,
			Namespace:          namespace,
			Labels:             researchLabels(sessionID, user),
			Annotations:        researchAnnotations(repoName, sessionID, cloneURL, htmlURL),
			Image:              image,
			Replicas:           1,
			WorkspaceDiskSize:  diskSize,
			EphemeralStorage:   ephemeralStorage,
			Secrets:            secrets,
			Env:                researchEnv(envs),
			ServiceAccountName: DeployerServiceAccount,
		},
	}

	fillEnvResources(&opt.DevSandboxOptions)
	sbObj, svc := NewAgentSandbox(opt)

	if _, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Create(ctx, sbObj, metav1.CreateOptions{}); err != nil {
		return "", fmt.Errorf("creating sandbox CR: %w", err)
	}
	if _, err := kubeClient.Clientset.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{}); err != nil {
		return "", fmt.Errorf("creating sandbox service: %w", err)
	}
	return name, nil
}
