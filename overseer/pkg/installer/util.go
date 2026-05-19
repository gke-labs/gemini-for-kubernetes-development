// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package installer

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"text/template"
)

func CheckTools() []error {
	var errs []error
	required := []string{"kubectl", "helm", "gcloud", "gh"}
	for _, bin := range required {
		if _, err := exec.LookPath(bin); err != nil {
			errs = append(errs, fmt.Errorf("required binary not found in PATH: %s\n  Install from: https://cloud.google.com/sdk/docs/install", bin))
		} else {
			fmt.Printf("  ✓ %s\n", bin)
		}
	}
	if len(errs) > 0 {
		return errs
	}
	return nil
}

func CheckGKEConnection(project, cluster, region string) error {
	fmt.Printf("  Fetching credentials for %s/%s/%s …\n", project, cluster, region)
	return run("gcloud", "container", "clusters", "get-credentials",
		cluster, "--location", region, "--project", project)
}

func InstallEnvoyProxy(version string) error {
	name := "envoy-gateway"
	chart := "oci://docker.io/envoyproxy/gateway-helm"
	namespace := "envoy-gateway-system"
	return helmInstall(name, chart, namespace, version)

}

func InstallAgentSandbox(version string) error {
	fmt.Println("  Installing AgentSandbox …")
	/** Currently having permission errors with the helm install
	name := "agent-sandbox"
	chart := "oci://ghcr.io/gke-labs/gemini-for-kubernetes-development/charts/agent-sandbox"
	namespace := "agent-sandbox-system"
	return helmInstall(name, chart, namespace, version)
	*/
	url := "https://github.com/kubernetes-sigs/agent-sandbox/releases/download/" + version + "/manifest.yaml"
	return kubectlInstall("agent-sandbox", url)
}

func InstallSecrets(ns, geminiAPIKey, gitHubPAT, gitHubOAuthClientID, gitHubOAuthSecret string) error {
	fmt.Println("  Creating gemini-api-key secret …")
	if err := applyNamespace(ns); err != nil {
		return err
	}

	keyMap := map[string]string{"key": geminiAPIKey}
	if err := applySecret(ns, "gemini-api-key", keyMap); err != nil {
		return err
	}

	fmt.Println("  Creating github-token secret …")
	tokenData := map[string]string{"token": gitHubPAT}
	if gitHubOAuthClientID != "" {
		tokenData["github-client-id"] = gitHubOAuthClientID
		tokenData["github-client-secret"] = gitHubOAuthSecret
		fmt.Println("  → multi-user mode (OAuth credentials included)")
	} else {
		fmt.Println("  → single-user mode (no OAuth credentials)")
	}
	if err := applySecret(ns, "github-token", tokenData); err != nil {
		return err
	}

	return nil
}

func InstallKRO(kroVersion string) error {
	fmt.Printf("  Installing kro …\n")
	cmd := []string{
		"helm", "upgrade", "kro", "oci://registry.k8s.io/kro/charts/kro", "--install",
		"--namespace", "kro-system", "--create-namespace",
		"--version", kroVersion, "--wait", "--timeout", "5m",
	}
	if err := run(cmd...); err != nil {
		return fmt.Errorf("helm install kro: %w", err)
	}
	return nil
}

func InstallCRDs() error {
	// Should look at deploying from source rather than release.
	fmt.Println("  Installing CRDs …")
	url := "https://github.com/gke-labs/gemini-for-kubernetes-development/releases/download/v0.1.0-rc.3/manifest.yaml"
	return kubectlInstall("gemini-for-kubernetes-development", url)
}

func InstallRepoWatch(watchNamespace, geminiAPIKey, gitHubPAT, gitHubOAuthClientID, gitHubOAuthSecret, repoURL string) error {
	// Create the namespace if it doesn't exist.
	if err := applyNamespace(watchNamespace); err != nil {
		return err
	}

	if err := InstallSecrets(watchNamespace, geminiAPIKey, gitHubPAT, gitHubOAuthClientID, gitHubOAuthSecret); err != nil {
		return err
	}

	tmpl, err := template.ParseFiles("templates/repowatch.tmpl")
	if err != nil {
		return err
	}
	data := struct {
		RepoName       string
		WatchNamespace string
		RepoURL        string
	}{
		RepoName:       repoSlug(repoURL),
		WatchNamespace: watchNamespace,
		RepoURL:        repoURL,
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return err
	}
	fmt.Printf("  Creating RepoWatch %s/%s …\n", watchNamespace, data.RepoName)
	//return applyYAML(buf.String())
	return nil
}

func applyNamespace(ns string) error {
	dry := []string{"kubectl", "create", "namespace", ns, "--dry-run=client", "-o", "yaml"}
	output, err := exec.Command(dry[0], dry[1:]...).Output()
	if err != nil {
		return fmt.Errorf("generate namespace %s: %w", ns, err)
	}
	if err := applyDryRun(string(output)); err != nil {
		return fmt.Errorf("applying namespace %s: %w", ns, err)
	}
	return nil
}

func applySecret(ns, name string, data map[string]string) error {
	dry := []string{"kubectl", "create", "secret", "generic", name, "-n", ns, "--dry-run=client", "-o", "yaml"}
	for key, val := range data {
		dry = append(dry, "--from-literal="+key+"="+val)
	}
	output, err := exec.Command(dry[0], dry[1:]...).Output()
	if err != nil {
		return fmt.Errorf("generating secret %s: %w", name, err)
	}
	if err := applyDryRun(string(output)); err != nil {
		return fmt.Errorf("applying secret %s: %w", name, err)
	}
	return nil
}

func applyDryRun(output string) error {
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(output)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func kubectlInstall(name, url string) error {
	fmt.Printf("  Installing %s …\n", name)
	cmd := []string{"kubectl", "apply", "-f", url}
	if err := run(cmd...); err != nil {
		return fmt.Errorf("kubectl install %s: %w", name, err)
	}
	return nil
}

func helmInstall(name, chart, namespace, version string) error {
	fmt.Printf("  Installing %s …\n", name)
	cmd := []string{
		"helm", "upgrade", "--install", name, chart,
		"--namespace", namespace, "--create-namespace",
		"--version", version, "--wait", "--timeout", "5m",
	}
	if err := run(cmd...); err != nil {
		log.Printf("%s %s %s %s %s %s %s %s %s %s %s %s %s",
			cmd[0], cmd[1], cmd[2], cmd[3], cmd[4], cmd[5], cmd[6],
			cmd[7], cmd[8], cmd[9], cmd[10], cmd[11], cmd[12])
		return fmt.Errorf("helm install %s: %w", name, err)
	}
	return nil
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

func run(args ...string) error {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
