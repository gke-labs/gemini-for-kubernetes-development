package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/acpd"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/constants"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/envd"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/tasks"
)

// ResearchReadyMarker prefixes the one machine-readable line `factory
// research start` prints. Everything else on stdout is progress for a
// human; a caller parses the line after this marker as JSON.
//
// A marker rather than a --output json mode because the surrounding
// commands all narrate their progress to stdout, and a caller that has
// to separate the two anyway is better served by an unmistakable
// prefix than by a flag that only half the output respects.
const ResearchReadyMarker = "RESEARCH_SANDBOX_READY "

// ResearchSandboxInfo is what the marker line carries: everything a
// caller needs to open a conversation, and nothing it could not
// otherwise get without another round trip to the API server.
type ResearchSandboxInfo struct {
	Sandbox   string `json:"sandbox"`
	Namespace string `json:"namespace"`
	SessionID string `json:"sessionId"`
	Repo      string `json:"repo"`
	// PodIP is how acpd is reached. Not a Service: acpd's port is not on
	// the sandbox's -lb Service, and adding it would change the manifest
	// every sandbox type shares.
	PodIP string `json:"podIP"`
	Port  int    `json:"port"`
	// CWD is the repository checkout the conversation should be about.
	CWD string `json:"cwd"`
}

// NewResearchCommand starts the sandbox a deep-research conversation
// runs in.
//
// This command does not hold the conversation. It creates the sandbox,
// gets the repository on disk and reports where acpd is listening; the
// caller then drives the session over HTTP. That split is deliberate —
// a conversation outlives any one CLI invocation, so the CLI cannot be
// the thing that owns it.
//
//	factory research start --url <repo> --session <id>
//
// Teardown is `factory sandbox delete <name>`; there is no research
// stop, because the sandbox is the whole of the session's state.
func NewResearchCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "research",
		Short: "Run deep-research conversation sandboxes",
	}

	var repoURL, sessionID string

	start := &cobra.Command{
		Use:   "start",
		Short: "Create a research sandbox and report where its conversation server is listening",
		RunE: func(c *cobra.Command, _ []string) error {
			if _, err := ResolveRootFlags(c); err != nil {
				return err
			}
			if repoURL == "" {
				return fmt.Errorf("--url is required")
			}
			if strings.TrimSpace(sessionID) == "" {
				return fmt.Errorf("--session is required")
			}
			if rootFlags.Timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, rootFlags.Timeout)
				defer cancel()
			}
			return runResearchStart(ctx, repoURL, sessionID)
		},
	}
	start.Flags().StringVar(&repoURL, "url", "", "Repository URL (https://github.com/owner/repo)")
	start.Flags().StringVar(&sessionID, "session", "", "Session identifier; determines the sandbox name")
	cmd.AddCommand(start)

	var saveURL, saveSession, saveNote string
	saveNotes := &cobra.Command{
		Use:   "save-notes",
		Short: "Push a research conversation's notes to the member's fork",
		Long: "Push what the conversation wrote to docs-exploration/research/<note>.md\n" +
			"to the " + tasks.ResearchNotesBranch + " branch of the member's fork.\n\n" +
			"Authoring is the conversation's job and pushing is this command's. The\n" +
			"session may be running with approvals turned off, so the credential is\n" +
			"held here for one command rather than left on the sandbox's disk where\n" +
			"the agent could reach it.",
		RunE: func(c *cobra.Command, _ []string) error {
			if _, err := ResolveRootFlags(c); err != nil {
				return err
			}
			if saveURL == "" {
				return fmt.Errorf("--url is required")
			}
			if strings.TrimSpace(saveSession) == "" {
				return fmt.Errorf("--session is required")
			}
			if rootFlags.Timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, rootFlags.Timeout)
				defer cancel()
			}
			return runResearchSaveNotes(ctx, saveURL, saveSession, saveNote)
		},
	}
	saveNotes.Flags().StringVar(&saveURL, "url", "", "Repository URL (https://github.com/owner/repo)")
	saveNotes.Flags().StringVar(&saveSession, "session", "", "Session identifier; names the sandbox to attach to")
	saveNotes.Flags().StringVar(&saveNote, "note", "", "File the notes were written to, under "+tasks.ResearchNotesDir+" (default: <session identifier>.md)")
	cmd.AddCommand(saveNotes)

	return cmd
}

func runResearchSaveNotes(ctx context.Context, repoURL, sessionID, note string) error {
	owner, repo, err := parseGitHubRepoURL(repoURL)
	if err != nil {
		return err
	}
	if !validRepoPart(sessionID) {
		return fmt.Errorf("unsupported --session %q: expected letters, digits, dash, underscore or dot", sessionID)
	}
	// A session's note is a file named after the session, not after its
	// identifier: the branch is read by people, months later, and an id
	// is not a name. The id remains the fallback for a conversation
	// nobody has named yet.
	if note == "" {
		note = sessionID + ".md"
	}
	// It names a file on the notes branch, so it is held to the same
	// character set as an owner or a repository — which also rules out
	// "." and ".." walking out of the directory the notes are meant to
	// be scoped to.
	if !validRepoPart(note) {
		return fmt.Errorf("unsupported --note %q: expected letters, digits, dash, underscore or dot", note)
	}

	kubeClient, err := clients.NewKubernetesClient()
	if err != nil {
		return fmt.Errorf("creating k8s client: %w", err)
	}
	secret, err := kubeClient.Clientset.CoreV1().Secrets(rootFlags.Namespace).Get(ctx, rootFlags.SecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("fetching %s secret in namespace %s: %w (make sure to run 'factory user onboard' first)", rootFlags.SecretName, rootFlags.Namespace, err)
	}
	script, err := tasks.GetSaveNotesScript()
	if err != nil {
		return fmt.Errorf("loading the save-notes script: %w", err)
	}

	// The sandbox has to already exist: this saves what a conversation
	// wrote, and there is no conversation without one.
	sandboxName := factorysandbox.ResearchSandboxName(repo, sessionID)
	fmt.Printf("Connecting to sandbox %s via envd...\n", sandboxName)
	client, err := envd.Connect(ctx, rootFlags.Namespace, sandboxName)
	if err != nil {
		return fmt.Errorf("connecting to sandbox %s: %w", sandboxName, err)
	}
	defer client.Close()

	login := string(secret.Data[constants.KeyGithubLogin])
	env := map[string]string{
		"HOME": "/workspaces/.home",
		// GH_TOKEN, not GITHUB_TOKEN: gh reads either, and the narrower
		// name keeps this from looking like the ambient credential the
		// rest of a sandbox's tooling picks up.
		"GH_TOKEN":          string(secret.Data[constants.KeyGithubToken]),
		"REPO_NAME":         repo,
		"UPSTREAM_REPO":     owner + "/" + repo,
		"SESSION_ID":        sessionID,
		"NOTES_FILE":        note,
		"GITHUB_USER_NAME":  login,
		"GITHUB_USER_EMAIL": string(secret.Data[constants.KeyGithubEmail]),
	}

	// Streamed rather than buffered: the fork can take a moment to
	// exist and the push is the slow part, so the narration is worth
	// having while it happens.
	var stderr bytes.Buffer
	if err := client.Exec(ctx, string(script), "/workspaces", env, nil, os.Stdout, io.MultiWriter(os.Stderr, &stderr)); err != nil {
		return fmt.Errorf("saving notes: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// parseGitHubRepoURL splits https://github.com/owner/repo[.git] and
// rejects anything whose owner or repository name is not GitHub's own
// character set — both halves reach a shell as a path and as part of a
// clone URL.
func parseGitHubRepoURL(repoURL string) (owner, repo string, err error) {
	u, perr := url.Parse(repoURL)
	if perr != nil {
		return "", "", fmt.Errorf("invalid repository URL: %w", perr)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return "", "", fmt.Errorf("expected URL format https://github.com/owner/repo, got %s", repoURL)
	}
	owner, repo = parts[0], strings.TrimSuffix(parts[1], ".git")
	if !validRepoPart(owner) || !validRepoPart(repo) {
		return "", "", fmt.Errorf("unsupported owner/repo in %s: expected GitHub-style names", repoURL)
	}
	return owner, repo, nil
}

func runResearchStart(ctx context.Context, repoURL, sessionID string) error {
	owner, repo, err := parseGitHubRepoURL(repoURL)
	if err != nil {
		return err
	}
	cloneURL := fmt.Sprintf("https://github.com/%s/%s.git", owner, repo)
	htmlURL := fmt.Sprintf("https://github.com/%s/%s", owner, repo)

	kubeClient, err := clients.NewKubernetesClient()
	if err != nil {
		return fmt.Errorf("creating k8s client: %w", err)
	}

	fmt.Printf("Ensuring research sandbox for %s/%s (session %s)...\n", owner, repo, sessionID)
	sandboxName, err := factorysandbox.EnsureResearchSandbox(ctx, kubeClient, rootFlags.Namespace, repo, sessionID, cloneURL, htmlURL, rootFlags.Image, rootFlags.DiskSize, rootFlags.EphemeralStorage, rootFlags.ResolvedSecrets, rootFlags.ResolvedEnvs, rootFlags.User)
	if err != nil {
		return fmt.Errorf("ensuring research sandbox: %w", err)
	}

	secret, err := kubeClient.Clientset.CoreV1().Secrets(rootFlags.Namespace).Get(ctx, rootFlags.SecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("fetching %s secret in namespace %s: %w (make sure to run 'factory user onboard' first)", rootFlags.SecretName, rootFlags.Namespace, err)
	}

	// Connecting waits for the pod to be ready and envd to answer, which
	// is also how long acpd needs: both are started by the same daemon.
	fmt.Printf("Connecting to sandbox %s via envd...\n", sandboxName)
	client, err := envd.Connect(ctx, rootFlags.Namespace, sandboxName)
	if err != nil {
		return fmt.Errorf("connecting to sandbox: %w", err)
	}
	defer client.Close()

	fmt.Printf("Preparing checkout of %s...\n", repo)
	if err := cloneForResearch(ctx, client, repo, cloneURL, secret.Data[constants.KeyGithubToken]); err != nil {
		return err
	}

	// Before the receipt, so that "ready" still means a session can be
	// created: acpd starts the engine at session create, and repo-agent
	// gives that call seconds, not the minutes a download can take.
	if rootFlags.Engine == "antigravity" {
		fmt.Printf("Installing agy_acp_server %s...\n", acpd.AntigravityACPServerVersion)
		if err := installAntigravityACPServer(ctx, client); err != nil {
			return err
		}
	}

	// The checkout is in place, so the sandbox is now worth keeping even
	// if the rest of this command fails. Write the receipt here rather
	// than at the end: what follows only reads the pod, and a sandbox
	// that is complete should not be torn down again because we could
	// not look up its IP.
	if err := factorysandbox.MarkResearchReady(ctx, kubeClient, rootFlags.Namespace, sandboxName); err != nil {
		return err
	}

	podIP, err := researchPodIP(ctx, kubeClient, rootFlags.Namespace, sandboxName)
	if err != nil {
		return err
	}

	info := ResearchSandboxInfo{
		Sandbox:   sandboxName,
		Namespace: rootFlags.Namespace,
		SessionID: sessionID,
		Repo:      repo,
		PodIP:     podIP,
		Port:      researchACPDPort,
		CWD:       "/workspaces/" + repo,
	}
	encoded, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("encoding sandbox info: %w", err)
	}
	fmt.Printf("%s%s\n", ResearchReadyMarker, encoded)
	return nil
}

// validRepoPart reports whether s is a GitHub owner or repository name:
// letters, digits, dot, dash, underscore, and not empty or a relative
// path component.
func validRepoPart(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// researchACPDPort mirrors acpd.DefaultPort. Duplicated rather than
// imported only to keep this file's imports to the command surface;
// they are the same constant and a change to one needs the other.
const researchACPDPort = 49984

// cloneForResearch puts the repository on the PVC.
//
// Deliberately the smaller half of tasks/lib.sh setupGitRepos: clone if
// absent, fetch if present. No fork, no default-repo, no branch reset —
// a research conversation reads the repository, and a reset would throw
// away work if the same sandbox is ever reused for one.
func cloneForResearch(ctx context.Context, client *envd.Client, repo, cloneURL string, githubToken []byte) error {
	// repo and cloneURL come from a user-supplied URL, so they reach the
	// shell as environment values and are referenced quoted. Interpolating
	// them into the script text would make a repository path with a
	// semicolon in it into arbitrary code running in the sandbox.
	const script = `set -e
if [ ! -d "/workspaces/${REPO_NAME}/.git" ]; then
  rm -rf "/workspaces/${REPO_NAME}"
  cd /workspaces && git clone "${CLONE_URL}"
else
  cd "/workspaces/${REPO_NAME}" && git fetch origin
fi`

	env := map[string]string{
		"HOME":         "/workspaces/.home",
		"GITHUB_TOKEN": string(githubToken),
		"REPO_NAME":    repo,
		"CLONE_URL":    cloneURL,
	}

	// Exec already runs the string under `sh -c`, so the script goes
	// through as-is; wrapping it in another shell would only add a layer
	// of quoting to get wrong.
	var stdout, stderr bytes.Buffer
	if err := client.Exec(ctx, script, "/workspaces", env, nil, &stdout, &stderr); err != nil {
		return fmt.Errorf("preparing checkout: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// installAntigravityACPServer puts Google's agy_acp_server where the
// acpd antigravity engine runs it from, unless it is already there.
//
// On the PVC rather than in the image: it is a gigabyte unpacked, and
// only a research sandbox on this engine needs it. The download and the
// unpack go to a sibling directory that is renamed into place last, so
// an interrupted install leaves nothing acpd would try to run, and the
// next start simply does it again.
func installAntigravityACPServer(ctx context.Context, client *envd.Client) error {
	env := antigravityInstallEnv(acpd.AntigravityACPServerDir, acpd.AntigravityACPServerURL, acpd.AntigravityACPServerSHA256)
	var stdout, stderr bytes.Buffer
	if err := client.Exec(ctx, antigravityInstallScript, "/workspaces", env, nil, &stdout, &stderr); err != nil {
		return fmt.Errorf("installing agy_acp_server: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// antigravityInstallScript runs under `sh -c` in the sandbox. python3
// rather than unzip, which the image does not carry; neither keeps the
// executable bit, hence the chmod.
const antigravityInstallScript = `set -e
if [ -x "${DEST}/agy_acp_server.par" ]; then
  exit 0
fi
tmp="${DEST}.partial"
rm -rf "${tmp}"
mkdir -p "${tmp}"
curl -fsSL --retry 3 -o "${tmp}/server.zip" "${URL}"
echo "${SHA256}  ${tmp}/server.zip" | sha256sum -c -
python3 -m zipfile -e "${tmp}/server.zip" "${tmp}"
rm "${tmp}/server.zip"
chmod +x "${tmp}/agy_acp_server.par" "${tmp}/localharness_external"
rm -rf "${DEST}"
mv "${tmp}" "${DEST}"`

func antigravityInstallEnv(dest, url, sha256 string) map[string]string {
	return map[string]string{
		"HOME":   "/workspaces/.home",
		"DEST":   dest,
		"URL":    url,
		"SHA256": sha256,
	}
}

// researchPodIP returns the sandbox pod's IP, retrying briefly: a pod
// can be Ready in envd's eyes a moment before its IP shows up in the
// object the API server hands back.
func researchPodIP(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, sandboxName string) (string, error) {
	for attempt := 0; attempt < 20; attempt++ {
		pods, err := kubeClient.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: "sandbox=" + sandboxName,
		})
		if err != nil {
			return "", fmt.Errorf("listing sandbox pods: %w", err)
		}
		for i := range pods.Items {
			pod := &pods.Items[i]
			if pod.DeletionTimestamp == nil && pod.Status.Phase == "Running" && pod.Status.PodIP != "" {
				return pod.Status.PodIP, nil
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return "", fmt.Errorf("sandbox %s has no running pod with an IP", sandboxName)
}
