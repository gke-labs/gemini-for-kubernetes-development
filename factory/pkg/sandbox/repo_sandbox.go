package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// A repository sandbox is the one a recipe on a repository declares by
// its prefix (recipe.SandboxDecl), one per key: <prefix>-<repo>-<hash of
// the key>. Whatever the recipe, these are the labels and annotations it
// carries, so a board finds a research session's, a run's or a repository
// recipe's the same way.
const (
	// LabelType is the sandbox's prefix (rsch, run, ...): what it is for.
	LabelType = "sandbox.gemini.google.com/type"
	// LabelKey is the key's short hash (ShortID): a label value has a charset and a length the key need not respect.
	LabelKey = "sandbox.gemini.google.com/key"
	// AnnotationKeyID is the key in full (a research session's id, a
	// run's name): the label cannot hold it, so this is the only place the
	// mapping from sandbox back to key survives.
	AnnotationKeyID = "sandbox.gemini.google.com/key-id"
	// AnnotationReady is the receipt for a finished setup, written by the
	// launch once its task is accepted (MarkReady), holding the time.
	//
	// The sandbox object is created in the first second of a launch that
	// runs for minutes, so its existence says only that something began.
	// Reading existence as completion retires a board's claim while the
	// clone is still to come, and an interrupted launch then leaves a
	// sandbox nothing will ever finish: a running pod, an empty workspace,
	// and an engine that cannot chdir into a checkout that was never made.
	// So completion gets its own mark: absent means the setup did not
	// finish and whoever was doing it is gone.
	AnnotationReady = "sandbox.gemini.google.com/ready"
	// AnnotationEngine is the acpd engine the sandbox was set up for,
	// written with the ready receipt. Whoever creates a conversation there
	// reads it from here rather than from wherever the choice was first
	// made, so a setting changed mid-launch cannot give a sandbox a
	// different engine than it was launched with.
	AnnotationEngine = "sandbox.gemini.google.com/engine"
)

// setupGrace is how long a repository sandbox may go without its ready
// receipt before EnsureRepoSandbox stops adopting it and replaces it
// instead.
//
// Set to the launch's own timeout, which is a bound rather than a
// guess: the board kills `factory recipe research` after twenty minutes,
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
const setupGrace = 20 * time.Minute

// sandboxReady reports whether a sandbox carries its ready receipt.
func sandboxReady(sb *unstructured.Unstructured) bool {
	return sb != nil && sb.GetAnnotations()[AnnotationReady] != ""
}

// disposition is what to do with a sandbox that already carries the
// name we want. A string, so a failing test names it.
type disposition string

const (
	// adopt: hand it to the caller as it stands.
	adopt disposition = "adopt"
	// replace: delete it and build a new one in its place.
	replace disposition = "replace"
)

// readyDisposition decides between the two, and is the whole policy for
// an interrupted launch.
//
// Ready is the only thing that protects a sandbox from replacement, so
// a finished session is never at risk however long it has been idle —
// age alone never condemns one.
func readyDisposition(sb *unstructured.Unstructured, now time.Time) disposition {
	switch {
	case sandboxReady(sb):
		return adopt
	case now.Sub(sb.GetCreationTimestamp().Time) < setupGrace:
		return adopt
	default:
		return replace
	}
}

// MarkReady writes a repository sandbox's ready receipt and the engine it was
// set up for: the launch handed it its task, so it is a session's from
// here, not an interrupted launch's husk. Called once, by the launch.
//
// A merge patch rather than the read-modify-Update the rest of this
// package uses, because this is the one annotation written from a
// different process than the controller that writes the others: a
// whole-object Update from here would race the board's kickoff stamp
// and could drop it.
func MarkReady(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, name, engine string) error {
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]string{
		AnnotationReady:  time.Now().UTC().Format(time.RFC3339),
		AnnotationEngine: engine,
	}}})
	if err != nil {
		return err
	}
	if _, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).
		Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("marking sandbox %s ready: %w", name, err)
	}
	return nil
}

// discardUnfinishedSandbox deletes a sandbox whose setup never
// finished, so the caller can build a fresh one under the same name.
//
// The companion Service goes too. It is created alongside the sandbox
// and carries no owner reference, so nothing would collect it, and the
// recreate would then fail on a name that is already taken.
func discardUnfinishedSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, name string) error {
	klog.Infof("sandbox %s has no ready receipt and is older than %s; replacing it", name, setupGrace)
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

// shortIDLen is how much of the key's digest goes in the name.
// Eight hex characters is 32 bits: enough that a collision between two
// live keys on one repo is not a practical concern, short enough to
// leave the repo slug readable in kubectl output.
const shortIDLen = 8

// ShortID is a key's short form, derived from it. Deriving it rather than
// minting it randomly makes creation idempotent: a caller that retries
// after a timeout computes the same name and finds the sandbox it already
// made, instead of starting a second engine for one conversation.
func ShortID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:shortIDLen]
}

// RepoSandboxName is a repository sandbox's name: <prefix>-<repo slug>-<short
// hash of the key>, the slug budgeted for the companion -lb Service's DNS
// cap. The same prefix and key always name the same sandbox, whatever
// recipe runs: that is how recipes of one prefix share one, and how a
// retried launch finds the one it made.
func RepoSandboxName(prefix, repo, key string) string {
	short := ShortID(key)
	slug := slugifyName(repo)
	if budget := 60 - len(prefix) - 1 - len(short) - 1; len(slug) > budget {
		slug = strings.Trim(slug[:budget], "-")
	}
	return prefix + "-" + slug + "-" + short
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

// RepoSandbox is a repository sandbox as its recipe declares it: its
// Prefix, the Key that names it, and its Size, which only the recipe sets.
type RepoSandbox struct {
	Prefix, Key string
	Size        Size
}

// Size is a repository sandbox's size, every field of it from the recipe.
type Size struct {
	Disk, EphemeralStorage                           string
	CPURequest, CPULimit, MemoryRequest, MemoryLimit string
}

// labels are a repository sandbox's labels: what it is, its key, and whose.
func (s RepoSandbox) labels(repoName, user string) map[string]string {
	return map[string]string{
		LabelType:                           s.Prefix,
		LabelKey:                            ShortID(s.Key),
		LabelRepo:                           labelValue(repoName),
		"factory.gemini.google.com/managed": "true",
		"factory.gemini.google.com/user":    user,
	}
}

// annotations record what the sandbox is for: its key, and which
// repository, in the repo/cloneURL/htmlURL keys every sandbox type writes.
func (s RepoSandbox) annotations(repoName, cloneURL, htmlURL string) map[string]string {
	return map[string]string{"repo": repoName, "cloneURL": cloneURL, "htmlURL": htmlURL, AnnotationKeyID: s.Key}
}

// EnsureRepoSandbox ensures a repository sandbox, RepoSandboxName(prefix,
// repo, key), as the deployer service account. Runs of any recipe of the
// prefix given the same key share it - a research session's transcript, a
// run's deployment live on its disk. A sandbox without its ready receipt
// is adopted while young and replaced once a husk (setupGrace).
func EnsureRepoSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, namespace string, s RepoSandbox, repoName, cloneURL, htmlURL, image, storageClass string, secrets []SecretMount, envs []EnvVar, user string) (string, error) {
	switch {
	case s.Prefix == "":
		return "", fmt.Errorf("a repository sandbox needs a prefix")
	case s.Key == "":
		return "", fmt.Errorf("a %s sandbox needs a key", s.Prefix)
	}
	name := RepoSandboxName(s.Prefix, repoName, s.Key)
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
	case err == nil && readyDisposition(sb, time.Now()) == adopt:
		// Finished, or unfinished and young enough that the launch which
		// made it is still running or died moments ago. Worth keeping:
		// the pod, the image pull and the disk are the expensive parts,
		// and everything after here is written to be re-run.
		prepareReusedSandbox(ctx, kubeClient, namespace, sb, user, image)
		return name, nil
	case err == nil:
		// Old and unfinished. Nothing is coming to complete it — a launch
		// cannot outlive setupGrace — so this is a husk: most
		// often a pod with an empty workspace, left by a launch that was
		// interrupted between creating the object and cloning into it.
		// Replace it rather than hand it over: the alternative is a
		// session stuck on "opening…" with no way out but deleting it.
		if derr := discardUnfinishedSandbox(ctx, kubeClient, namespace, name); derr != nil {
			return "", derr
		}
	case !strings.Contains(err.Error(), "not found"):
		return "", fmt.Errorf("checking sandbox existence: %w", err)
	}

	opt := AgentSandboxOptions{
		DevSandboxOptions: DevSandboxOptions{
			Name:                  name,
			Namespace:             namespace,
			Labels:                s.labels(repoName, user),
			Annotations:           s.annotations(repoName, cloneURL, htmlURL),
			Image:                 image,
			Replicas:              1,
			WorkspaceDiskSize:     s.Size.Disk,
			WorkspaceStorageClass: storageClass,
			EphemeralStorage:      s.Size.EphemeralStorage,
			CPURequest:            s.Size.CPURequest,
			CPULimit:              s.Size.CPULimit,
			MemoryRequest:         s.Size.MemoryRequest,
			MemoryLimit:           s.Size.MemoryLimit,
			Secrets:               secrets,
			Env:                   envs,
			ServiceAccountName:    DeployerServiceAccount,
		},
	}
	sbObj, svc := NewAgentSandbox(opt)
	if err := createSandbox(ctx, kubeClient, namespace, sbObj, svc); err != nil {
		return "", err
	}
	return name, nil
}
