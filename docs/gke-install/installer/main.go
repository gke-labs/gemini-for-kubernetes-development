// Command gfk-install is an interactive installer for Gemini for Kubernetes
// Development on Google Kubernetes Engine (GKE).
//
// Usage:
//
//	go run . [flags]
//	./gfk-install [flags]
//
// All flags are optional; the installer will prompt for any values not supplied.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"text/template"
	"time"
)

const (
	releaseVersion  = "v0.1.0-rc.3"
	manifestURL     = "https://github.com/gke-labs/gemini-for-kubernetes-development/releases/download/" + releaseVersion + "/manifest.yaml"
	systemNamespace = "repo-agent-system"
)

// Config holds every value the installer needs.
type Config struct {
	// GCP / GKE
	Project string
	Cluster string
	Region  string

	// Repository to watch
	RepoURL string
	// Namespace the RepoWatch CR lives in (defaults to a slug of the repo name)
	WatchNamespace string

	// Credentials
	GeminiAPIKey        string
	GitHubPAT           string
	GitHubOAuthClientID string // empty → single-user mode
	GitHubOAuthSecret   string

	// Git identity used by the bot for commits
	BotGitName  string
	BotGitEmail string

	// GitHub logins that may administer the UI (comma-separated)
	AdminUsers string

	// Whether to install Kyverno (required on GKE; can skip if already present)
	InstallKyverno bool
}

func main() {
	cfg := &Config{}

	flag.StringVar(&cfg.Project, "project", "", "GCP project ID")
	flag.StringVar(&cfg.Cluster, "cluster", "", "GKE cluster name")
	flag.StringVar(&cfg.Region, "region", "us-central1", "GKE cluster region/zone")
	flag.StringVar(&cfg.RepoURL, "repo", "", "GitHub repository URL to watch (e.g. https://github.com/org/repo)")
	flag.StringVar(&cfg.WatchNamespace, "watch-namespace", "", "Namespace for RepoWatch CR (default: derived from repo name)")
	flag.StringVar(&cfg.GeminiAPIKey, "gemini-api-key", "", "Google Gemini API key")
	flag.StringVar(&cfg.GitHubPAT, "github-pat", "", "GitHub Personal Access Token for the bot account")
	flag.StringVar(&cfg.GitHubOAuthClientID, "oauth-client-id", "", "GitHub OAuth App client ID (omit for single-user mode)")
	flag.StringVar(&cfg.GitHubOAuthSecret, "oauth-client-secret", "", "GitHub OAuth App client secret")
	flag.StringVar(&cfg.BotGitName, "bot-name", "", "Git author name for bot commits")
	flag.StringVar(&cfg.BotGitEmail, "bot-email", "", "Git author email for bot commits")
	flag.StringVar(&cfg.AdminUsers, "admin-users", "", "Comma-separated GitHub logins allowed to administer the UI")
	flag.BoolVar(&cfg.InstallKyverno, "install-kyverno", true, "Install Kyverno (required on GKE for image-reference rewriting)")
	flag.Parse()

	banner()

	r := bufio.NewReader(os.Stdin)
	prompt := func(label, def *string, desc string) {
		if *label != "" {
			return
		}
		fmt.Printf("  %s", desc)
		if *def != "" {
			fmt.Printf(" [%s]", *def)
		}
		fmt.Print(": ")
		line, _ := r.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			line = *def
		}
		*label = line
	}
	promptSecret := func(label *string, desc string) {
		if *label != "" {
			return
		}
		fmt.Printf("  %s: ", desc)
		line, _ := r.ReadString('\n')
		*label = strings.TrimSpace(line)
	}

	fmt.Println("\n── GCP / GKE ──────────────────────────────────────────────────────")
	prompt(&cfg.Project, strPtr(""), "GCP project ID")
	prompt(&cfg.Cluster, strPtr(""), "GKE cluster name")
	prompt(&cfg.Region, strPtr("us-central1"), "Region / zone")

	fmt.Println("\n── Repository ─────────────────────────────────────────────────────")
	prompt(&cfg.RepoURL, strPtr(""), "GitHub repo URL to watch")
	if cfg.WatchNamespace == "" {
		cfg.WatchNamespace = repoSlug(cfg.RepoURL)
	}
	prompt(&cfg.WatchNamespace, strPtr(cfg.WatchNamespace), "Kubernetes namespace for RepoWatch CR")

	fmt.Println("\n── Credentials ────────────────────────────────────────────────────")
	promptSecret(&cfg.GeminiAPIKey, "Gemini API key")
	promptSecret(&cfg.GitHubPAT, "GitHub PAT (bot account, needs repo + workflow scopes)")
	fmt.Print("  GitHub OAuth App client ID (leave blank for single-user mode): ")
	line, _ := r.ReadString('\n')
	cfg.GitHubOAuthClientID = strings.TrimSpace(line)
	if cfg.GitHubOAuthClientID != "" {
		promptSecret(&cfg.GitHubOAuthSecret, "GitHub OAuth App client secret")
	}

	fmt.Println("\n── Bot identity ───────────────────────────────────────────────────")
	prompt(&cfg.BotGitName, strPtr(""), "Bot git author name")
	prompt(&cfg.BotGitEmail, strPtr(""), "Bot git author email")
	prompt(&cfg.AdminUsers, strPtr(""), "Admin GitHub logins (comma-separated, may be blank)")

	fmt.Println()
	if !confirm(r, "Proceed with installation?") {
		fmt.Println("Aborted.")
		os.Exit(0)
	}

	steps := []step{
		{"Check prerequisites", checkPrereqs},
		{"Connect kubectl to GKE cluster", func(c *Config) error { return connectGKE(c) }},
		{"Uninstall KRO (if present)", uninstallKro},
		{"Install Kyverno", maybeInstallKyverno},
		{"Install Helm dependencies (Envoy Gateway, Agent Sandbox)", installHelmDeps},
		{"Apply release manifest (" + releaseVersion + ")", applyManifest},
		{"Create Kubernetes secrets", createSecrets},
		{"Apply GKE compatibility fixes (image rewriting + gh wrapper)", applyGKEFixes},
		{"Create RepoWatch CR", createRepoWatch},
		{"Wait for system readiness", waitReady},
		{"Print next steps", printNextSteps},
	}

	for i, s := range steps {
		fmt.Printf("\n[%d/%d] %s\n", i+1, len(steps), s.name)
		fmt.Println(strings.Repeat("─", 60))
		if err := s.fn(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "\n✗ Step failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✓ Done\n")
	}
}

// ── Steps ─────────────────────────────────────────────────────────────────────

func checkPrereqs(cfg *Config) error {
	required := []string{"kubectl", "helm", "gcloud", "gh"}
	for _, bin := range required {
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("required binary not found in PATH: %s\n  Install from: https://cloud.google.com/sdk/docs/install", bin)
		}
		fmt.Printf("  ✓ %s\n", bin)
	}
	return nil
}

func connectGKE(cfg *Config) error {
	fmt.Printf("  Fetching credentials for %s/%s …\n", cfg.Project, cfg.Cluster)
	return run("gcloud", "container", "clusters", "get-credentials",
		cfg.Cluster, "--location", cfg.Region, "--project", cfg.Project)
}

func uninstallKro(cfg *Config) error {
	fmt.Println("  Uninstalling KRO (if present) …")
	// Try to uninstall via helm first
	_ = run("helm", "uninstall", "kro", "--namespace", "kro")
	// Then delete the namespace
	_ = run("kubectl", "delete", "namespace", "kro", "--ignore-not-found")
	return nil
}

func maybeInstallKyverno(cfg *Config) error {
	if !cfg.InstallKyverno {
		fmt.Println("  Skipping (--install-kyverno=false)")
		return nil
	}
	// Check if already installed
	out, _ := output("kubectl", "get", "deployment", "-n", "kyverno", "kyverno-admission-controller", "--ignore-not-found")
	if strings.Contains(out, "kyverno-admission-controller") {
		fmt.Println("  Kyverno already installed, skipping")
		return nil
	}
	fmt.Println("  Installing Kyverno via Helm …")
	if err := run("helm", "repo", "add", "kyverno", "https://kyverno.github.io/kyverno/"); err != nil {
		return err
	}
	if err := run("helm", "repo", "update"); err != nil {
		return err
	}
	return run("helm", "upgrade", "--install", "kyverno", "kyverno/kyverno",
		"--namespace", "kyverno", "--create-namespace",
		"--set", "admissionController.replicas=1",
		"--wait", "--timeout", "5m")
}

func installHelmDeps(cfg *Config) error {
	type helmChart struct {
		name, repo, chart, namespace, version string
		extraArgs                             []string
	}
	charts := []helmChart{
		{
			name: "envoy-gateway", repo: "envoyproxy",
			chart:     "oci://docker.io/envoyproxy/gateway-helm",
			namespace: "envoy-gateway-system", version: "v1.5.2",
		},
		{
			name: "agent-sandbox", repo: "agent-sandbox",
			chart:     "oci://ghcr.io/gke-labs/gemini-for-kubernetes-development/charts/agent-sandbox",
			namespace: "agent-sandbox-system", version: releaseVersion,
		},
	}

	for _, c := range charts {
		fmt.Printf("  Installing %s …\n", c.name)
		args := []string{
			"upgrade", "--install", c.name, c.chart,
			"--namespace", c.namespace, "--create-namespace",
			"--version", c.version,
			"--wait", "--timeout", "5m",
		}
		args = append(args, c.extraArgs...)
		if err := run(append([]string{"helm"}, args...)...); err != nil {
			return fmt.Errorf("helm install %s: %w", c.name, err)
		}
	}
	return nil
}

func applyManifest(_ *Config) error {
	fmt.Printf("  Applying %s …\n", manifestURL)
	return run("kubectl", "apply", "-f", manifestURL)
}

func createSecrets(cfg *Config) error {
	ns := systemNamespace

	fmt.Println("  Creating gemini-api-key secret …")
	if err := applySecret(ns, "gemini-api-key", map[string]string{
		"key": cfg.GeminiAPIKey,
	}); err != nil {
		return err
	}

	fmt.Println("  Creating github-token secret …")
	tokenData := map[string]string{
		"token": cfg.GitHubPAT,
	}
	if cfg.GitHubOAuthClientID != "" {
		tokenData["github-client-id"] = cfg.GitHubOAuthClientID
		tokenData["github-client-secret"] = cfg.GitHubOAuthSecret
		fmt.Println("  → multi-user mode (OAuth credentials included)")
	} else {
		fmt.Println("  → single-user mode (no OAuth credentials)")
	}
	if err := applySecret(ns, "github-token", tokenData); err != nil {
		return err
	}

	return nil
}

// kyvernoPolicy is the ClusterPolicy that:
//  1. Rewrites ko:// build-time image references to real ghcr.io paths at runtime
//  2. Mounts the devcontainer-json ConfigMap into PR review sandbox pods so
//     envbuilder can find devcontainer.json at /devcontainer.json
const kyvernoPolicy = `apiVersion: kyverno.io/v1
kind: ClusterPolicy
metadata:
  name: gfk-gke-compat
  annotations:
    policies.kyverno.io/description: |
      GKE compatibility fixes for gemini-for-kubernetes-development.
      (1) Rewrites ko:// build-time image references to real ghcr.io runtime images.
      (2) Mounts devcontainer-json ConfigMap into envbuilder PR-review pods.
spec:
  rules:
  - name: replace-repo-sandbox-initcontainer
    match:
      any:
      - resources:
          kinds: [Pod]
    mutate:
      foreach:
      - list: "request.object.spec.initContainers || ` + "`[]`" + `"
        order: Ascending
        preconditions:
          all:
          - key: "{{element.image}}"
            operator: Equals
            value: "ko://repo-agent/images/repo-sandbox"
        patchesJson6902: |-
          - op: replace
            path: /spec/initContainers/{{elementIndex}}/image
            value: "ghcr.io/gke-labs/gemini-for-kubernetes-development/repo-sandbox:latest"
    skipBackgroundRequests: true
  - name: replace-sandbox-main-container
    match:
      any:
      - resources:
          kinds: [Pod]
    mutate:
      foreach:
      - list: "request.object.spec.containers || ` + "`[]`" + `"
        order: Ascending
        preconditions:
          all:
          - key: "{{element.image}}"
            operator: Equals
            value: "ko://repo-agent/images/repo-sandbox"
        patchesJson6902: |-
          - op: replace
            path: /spec/containers/{{elementIndex}}/image
            value: "ghcr.io/gke-labs/gemini-for-kubernetes-development/generic-golang:latest"
    skipBackgroundRequests: true
  - name: replace-configdir-initcontainer
    match:
      any:
      - resources:
          kinds: [Pod]
    mutate:
      foreach:
      - list: "request.object.spec.initContainers || ` + "`[]`" + `"
        order: Ascending
        preconditions:
          all:
          - key: "{{element.image}}"
            operator: Equals
            value: "ko://repo-agent/images/configdir-cli"
        patchesJson6902: |-
          - op: replace
            path: /spec/initContainers/{{elementIndex}}/image
            value: "ghcr.io/gke-labs/gemini-for-kubernetes-development/configdir-cli:latest"
    skipBackgroundRequests: true
  - name: replace-overseer-container
    match:
      any:
      - resources:
          kinds: [Pod]
    mutate:
      foreach:
      - list: "request.object.spec.containers || ` + "`[]`" + `"
        order: Ascending
        preconditions:
          all:
          - key: "{{element.image}}"
            operator: Equals
            value: "ko://overseer/images/overseer"
        patchesJson6902: |-
          - op: replace
            path: /spec/containers/{{elementIndex}}/image
            value: "ghcr.io/gke-labs/gemini-for-kubernetes-development/overseer:latest"
    skipBackgroundRequests: true
  - name: mount-devcontainer-json
    match:
      any:
      - resources:
          kinds: [Pod]
          namespaces: [repo-agent-system]
    preconditions:
      all:
      - key: "{{ request.object.spec.containers[?name=='sandbox'] | length(@) }}"
        operator: GreaterThan
        value: "0"
    mutate:
      patchStrategicMerge:
        spec:
          volumes:
          - name: devcontainer-json
            configMap:
              name: devcontainer-json
              items:
              - key: devcontainer.json
                path: devcontainer.json
          containers:
          - name: sandbox
            volumeMounts:
            - name: devcontainer-json
              mountPath: /devcontainer.json
              subPath: devcontainer.json
  validationFailureAction: Audit
`

// devcontainerJSON is the devcontainer.json content patched into the
// devcontainer-json ConfigMap in repo-agent-system.  It uses the pre-built
// generic-golang image (avoiding a multi-minute envbuilder build on cold
// clusters) and installs a gh wrapper that force-resets the working branch to
// upstream instead of attempting a fast-forward pull (which fails when the
// sandbox branch has diverged from the upstream PR branch).
const devcontainerJSON = `{
  "name": "Go and Node.js Dev Container",
  "image": "ghcr.io/gke-labs/gemini-for-kubernetes-development/generic-golang:latest",
  "postCreateCommand": "git config --global pull.rebase true && git config --global rebase.autoStash true && { [ -f /usr/bin/gh ] && [ ! -f /usr/bin/gh-real ] && mv /usr/bin/gh /usr/bin/gh-real || true; } && printf '#!/bin/bash\nif [[ \"${1}\" == \"pr\" ]] && [[ \"${2}\" == \"checkout\" ]]; then\n  BRANCH=$(/usr/bin/gh-real pr view \"${3}\" --json headRefName -q .headRefName 2>/dev/null)\n  if [ -n \"${BRANCH}\" ]; then\n    git fetch upstream --quiet 2>/dev/null || git fetch origin --quiet 2>/dev/null || true\n    git checkout \"${BRANCH}\" 2>/dev/null || git checkout -b \"${BRANCH}\" \"upstream/${BRANCH}\" 2>/dev/null || true\n    git reset --hard \"upstream/${BRANCH}\" 2>/dev/null && exit 0\n    git reset --hard \"origin/${BRANCH}\" 2>/dev/null && exit 0\n  fi\nfi\nexec /usr/bin/gh-real \"$@\"\n' > /usr/local/bin/gh && chmod +x /usr/local/bin/gh"
}
`

func applyGKEFixes(cfg *Config) error {
	fmt.Println("  Applying Kyverno ClusterPolicy (image rewriting + devcontainer mount) …")
	if err := applyYAML(kyvernoPolicy); err != nil {
		return fmt.Errorf("apply Kyverno policy: %w", err)
	}

	fmt.Println("  Patching devcontainer-json ConfigMap with generic-golang base + gh wrapper …")
	// Build the JSON patch value
	patchVal, err := json.Marshal(map[string]map[string]string{
		"data": {"devcontainer.json": devcontainerJSON},
	})
	if err != nil {
		return err
	}
	if err := run("kubectl", "patch", "configmap", "-n", systemNamespace,
		"devcontainer-json", "--type=merge", "-p", string(patchVal)); err != nil {
		return fmt.Errorf("patch devcontainer-json: %w", err)
	}

	// Bounce any existing PR-review sandbox pods that were started before the
	// policy existed so they recreate with the correct image and the ConfigMap
	// volume injected.
	fmt.Println("  Bouncing pre-existing sandbox pods to pick up new policy …")
	_ = run("kubectl", "delete", "pods", "-n", systemNamespace,
		"-l", "sandbox.gemini.google.com/type=review", "--ignore-not-found")

	return nil
}

const repoWatchTmpl = `apiVersion: review.gemini.google.com/v1alpha1
kind: RepoWatch
metadata:
  name: {{ .RepoName }}
  namespace: {{ .WatchNamespace }}
spec:
  repoURL: {{ .RepoURL }}
  githubSecretName: github-token
  pollIntervalSeconds: 60
  review:
    llm:
      provider: gemini-cli
      apiKeySecretRef: gemini-api-key
    maxActiveSandboxes: 2
    workspaceDiskSize: 10Gi
  issue:
    llm:
      provider: gemini-cli
      apiKeySecretRef: gemini-api-key
    maxActiveSandboxes: 2
    workspaceDiskSize: 10Gi
`

func createRepoWatch(cfg *Config) error {
	// Create the namespace if it doesn't exist.
	_ = run("kubectl", "create", "namespace", cfg.WatchNamespace, "--dry-run=client", "-o", "yaml")
	if err := run("kubectl", "create", "namespace", cfg.WatchNamespace); err != nil {
		// Ignore "already exists" errors.
		if !strings.Contains(err.Error(), "already exists") {
			fmt.Printf("  Note: namespace may already exist (%v)\n", err)
		}
	}

	// Copy the github-token and gemini-api-key secrets into the watch namespace
	// so the RepoWatch controller can access them.
	for _, secret := range []string{"github-token", "gemini-api-key"} {
		fmt.Printf("  Copying secret %s → %s …\n", secret, cfg.WatchNamespace)
		secretJSON, err := output("kubectl", "get", "secret", "-n", systemNamespace, secret, "-o", "json")
		if err != nil {
			return fmt.Errorf("get secret %s: %w", secret, err)
		}
		// Clear metadata that would prevent re-creation in another namespace.
		var obj map[string]interface{}
		if err := json.Unmarshal([]byte(secretJSON), &obj); err != nil {
			return err
		}
		if meta, ok := obj["metadata"].(map[string]interface{}); ok {
			delete(meta, "resourceVersion")
			delete(meta, "uid")
			delete(meta, "creationTimestamp")
			delete(meta, "annotations")
			meta["namespace"] = cfg.WatchNamespace
		}
		patchedJSON, _ := json.Marshal(obj)
		if err := applyYAML(string(patchedJSON)); err != nil {
			fmt.Printf("  Warning: could not copy secret (may already exist): %v\n", err)
		}
	}

	// Render and apply the RepoWatch CR.
	tmpl, err := template.New("rw").Parse(repoWatchTmpl)
	if err != nil {
		return err
	}
	data := struct {
		RepoName       string
		WatchNamespace string
		RepoURL        string
	}{
		RepoName:       repoSlug(cfg.RepoURL),
		WatchNamespace: cfg.WatchNamespace,
		RepoURL:        cfg.RepoURL,
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return err
	}
	fmt.Printf("  Creating RepoWatch %s/%s …\n", cfg.WatchNamespace, data.RepoName)
	return applyYAML(buf.String())
}

func waitReady(_ *Config) error {
	resources := []struct{ kind, ns, name string }{
		{"deployment", systemNamespace, "pr-review-api"},
		{"deployment", systemNamespace, "pr-review-ui"},
		{"deployment", systemNamespace, "github-mcp-server"},
		{"statefulset", systemNamespace, "repowatch-controller"},
	}
	for _, r := range resources {
		fmt.Printf("  Waiting for %s/%s …\n", r.kind, r.name)
		if err := run("kubectl", "rollout", "status", r.kind+"/"+r.name,
			"-n", r.ns, "--timeout=5m"); err != nil {
			return fmt.Errorf("%s %s: %w", r.kind, r.name, err)
		}
	}
	return nil
}

const nextStepsTmpl = `
╔══════════════════════════════════════════════════════════════════╗
║              Installation complete!                              ║
╚══════════════════════════════════════════════════════════════════╝

UI endpoint:
  kubectl get svc -n {{ .NS }} pr-review-ui -o jsonpath='{.status.loadBalancer.ingress[0].ip}'

Log in:
  Visit https://<EXTERNAL_IP>/api/auth/login  (GitHub OAuth)
  — or, in single-user mode, the UI may show directly.

Monitor your RepoWatch:
  kubectl get repowatch -n {{ .WatchNS }} -w

View sandbox pods:
  kubectl get pods -n {{ .WatchNS }}

Troubleshooting:
  kubectl logs -n {{ .NS }} -l app=repowatch-controller -f
  kubectl logs -n {{ .NS }} -l app=pr-review-api -f

Documentation:
  https://github.com/gke-labs/gemini-for-kubernetes-development/blob/main/docs/gke-install/README.md
`

func printNextSteps(cfg *Config) error {
	tmpl, _ := template.New("ns").Parse(nextStepsTmpl)
	return tmpl.Execute(os.Stdout, map[string]string{
		"NS":      systemNamespace,
		"WatchNS": cfg.WatchNamespace,
	})
}

// ── Helpers ────────────────────────────────────────────────────────────────────

type step struct {
	name string
	fn   func(*Config) error
}

func banner() {
	fmt.Print(`
╔══════════════════════════════════════════════════════════════════╗
║  Gemini for Kubernetes Development — GKE Installer               ║
║  ` + releaseVersion + `                                                      ║
╚══════════════════════════════════════════════════════════════════╝

This installer deploys the repo-agent stack onto an existing GKE
cluster and configures the GKE-specific compatibility fixes needed
for envbuilder image resolution and PR branch management.

Prerequisites:
  • An existing GKE cluster (Standard or Autopilot)
  • kubectl, helm, gcloud, gh configured and in PATH
  • A GitHub Personal Access Token (repo + workflow scopes)
  • A Google Gemini API key
  • (Optional) A GitHub OAuth App for multi-user UI access
`)
}

func confirm(r *bufio.Reader, msg string) bool {
	fmt.Printf("%s [y/N]: ", msg)
	line, _ := r.ReadString('\n')
	return strings.ToLower(strings.TrimSpace(line)) == "y"
}

func run(args ...string) error {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func output(args ...string) (string, error) {
	cmd := exec.Command(args[0], args[1:]...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = os.Stderr
	return buf.String(), cmd.Run()
}

func applyYAML(yaml string) error {
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(yaml)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func applySecret(ns, name string, data map[string]string) error {
	args := []string{
		"create", "secret", "generic", name,
		"-n", ns,
		"--dry-run=client", "-o", "yaml",
	}
	for k, v := range data {
		args = append(args, "--from-literal="+k+"="+v)
	}
	yamlBytes, err := exec.Command("kubectl", args...).Output()
	if err != nil {
		return fmt.Errorf("generate secret %s: %w", name, err)
	}
	return applyYAML(string(yamlBytes))
}

func repoSlug(repoURL string) string {
	parts := strings.Split(strings.TrimSuffix(repoURL, ".git"), "/")
	if len(parts) == 0 {
		return "repo-watch"
	}
	slug := parts[len(parts)-1]
	slug = strings.ToLower(slug)
	// Replace non-alphanumeric with hyphens
	var b strings.Builder
	for _, c := range slug {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			b.WriteRune(c)
		} else {
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func strPtr(s string) *string { return &s }

// Ensure time is imported (used by waitReady indirectly via helm --timeout).
var _ = time.Second
