package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

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
	// AnnotationResearchReady is the receipt for a finished setup:
	// written last by `factory research start`, holding the time it
	// finished.
	//
	// The sandbox object is created in the first second of a launch that
	// runs for minutes, so its existence says only that something began.
	// Reading existence as completion — which the board's Request did —
	// retires the claim while the clone is still to come, and an
	// interrupted launch then leaves a sandbox nothing will ever finish:
	// a running pod, an empty workspace, and an engine that cannot chdir
	// into a checkout that was never made.
	//
	// So completion gets its own mark, under the rule the kickoff and the
	// capture already use: the annotation IS the receipt. Absent means
	// the setup did not finish and whoever was doing it is gone.
	AnnotationResearchReady = "sandbox.gemini.google.com/research-ready"
)

// researchSetupGrace is how long a sandbox may go without its ready
// receipt before EnsureResearchSandbox stops adopting it and replaces
// it instead.
//
// Set to the launch's own timeout, which is a bound rather than a
// guess: the board kills `factory research start` after twenty minutes,
// so a sandbox that has gone this long without a receipt cannot still
// have anyone working on it. (The timeout itself is repo-agent's
// factorycli.researchTimeout, and these two modules do not import each
// other, so the number is mirrored here the way the annotation strings
// are. Raising one without the other only widens or narrows the window
// in which a husk is adopted rather than replaced.)
//
// The bound matters most when the controller is restarting repeatedly,
// which is exactly when interrupted launches happen: a policy that
// replaced the sandbox on every relaunch would tear down and rebuild a
// pod that each new controller was about to finish, and never converge.
const researchSetupGrace = 20 * time.Minute

// researchSandboxReady reports whether a sandbox finished its setup.
func researchSandboxReady(sb *unstructured.Unstructured) bool {
	return sb != nil && sb.GetAnnotations()[AnnotationResearchReady] != ""
}

// disposition is what to do with a sandbox that already carries the
// name we want. A string, so a failing test names it.
type disposition string

const (
	// researchAdopt: hand it to the caller as it stands.
	researchAdopt disposition = "adopt"
	// researchReplace: delete it and build a new one in its place.
	researchReplace disposition = "replace"
)

// researchDisposition decides between the two, and is the whole policy
// for an interrupted launch.
//
// Ready is the only thing that protects a sandbox from replacement, so
// a finished session is never at risk however long it has been idle —
// age alone never condemns one.
func researchDisposition(sb *unstructured.Unstructured, now time.Time) disposition {
	switch {
	case researchSandboxReady(sb):
		return researchAdopt
	case now.Sub(sb.GetCreationTimestamp().Time) < researchSetupGrace:
		return researchAdopt
	default:
		return researchReplace
	}
}

// MarkResearchReady writes the receipt that says this sandbox's setup
// finished. Called once, by the launch, after the checkout is in place.
//
// A merge patch rather than the read-modify-Update the rest of this
// package uses, because this is the one annotation written from a
// different process than the controller that writes the others: a
// whole-object Update from here would race the board's kickoff stamp
// and could drop it.
func MarkResearchReady(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, name string) error {
	patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`,
		AnnotationResearchReady, time.Now().UTC().Format(time.RFC3339))
	_, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).
		Patch(ctx, name, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("marking sandbox %s ready: %w", name, err)
	}
	return nil
}

// discardUnfinishedResearchSandbox deletes a sandbox whose setup never
// finished, so the caller can build a fresh one under the same name.
//
// The companion Service goes too. It is created alongside the sandbox
// and carries no owner reference, so nothing would collect it, and the
// recreate would then fail on a name that is already taken.
func discardUnfinishedResearchSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, name string) error {
	klog.Infof("sandbox %s has no ready receipt and is older than %s; replacing it", name, researchSetupGrace)
	err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting unfinished sandbox %s: %w", name, err)
	}
	err = kubeClient.Clientset.CoreV1().Services(namespace).Delete(ctx, name+"-lb", metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting service for unfinished sandbox %s: %w", name, err)
	}
	return awaitSandboxGone(ctx, kubeClient, namespace, name)
}

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
// One per conversation rather than one per repo: the transcript lives
// on the PVC and dies with the sandbox, so sharing one between sessions
// would mean sharing a transcript.
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
	case err == nil && researchDisposition(sb, time.Now()) == researchAdopt:
		// Either finished, or unfinished and young enough that the launch
		// which made it is still running or died moments ago. Both are
		// worth keeping: the pod, the image pull and the disk are the
		// expensive parts and they are already paid for, and everything
		// this command does after here is written to be re-run, so
		// finishing someone else's setup is the same code as doing our
		// own.
		ensureSandboxUserLabel(ctx, kubeClient, namespace, sb, user)
		return name, nil
	case err == nil:
		// Old and unfinished. Nothing is coming to complete it — a launch
		// cannot outlive researchSetupGrace — so this is a husk: most
		// often a pod with an empty workspace, left by a launch that was
		// interrupted between creating the object and cloning into it.
		// Replace it rather than hand it over.
		//
		// The cost is that a launch killed in the instant between the
		// clone and the receipt loses a sandbox that was in fact usable.
		// It is rare, it recovers by rebuilding, and the alternative is
		// the failure this receipt exists to end: a session stuck on
		// "opening…" with no way out but deleting it.
		if derr := discardUnfinishedResearchSandbox(ctx, kubeClient, namespace, name); derr != nil {
			return "", derr
		}
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
	// An already-existing Service is not a failure. It carries no owner
	// reference, so a sandbox deleted by any path other than
	// discardUnfinishedResearchSandbox — a `kubectl delete sandbox`, the
	// terminating branch above — leaves its Service behind, and the name
	// and selector are derived from the sandbox name, so the one that
	// survived is the one we would have made.
	if _, err := kubeClient.Clientset.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("creating sandbox service: %w", err)
	}
	return name, nil
}
