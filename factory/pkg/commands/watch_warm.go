package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/recipe"
	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskapi"
)

// warmScriptMaxBytes caps a script downloaded from scriptURL.
const warmScriptMaxBytes = 1 << 20

// warmScriptClient downloads warm scripts; tests replace it.
var warmScriptClient = &http.Client{Timeout: time.Minute}

// warmSnapshotTTLIntervals is how many intervals a warm snapshot is
// restored from: past that, warming has stopped, and restoring a disk
// that old would cost more than it saves.
const warmSnapshotTTLIntervals = 3

// warmWorkspace moves repoURL's warm cycle (design/warm-workspace.md) on
// by one step, from the state its warm sandbox records:
//
//   - a snapshot made: once it is ready, keep the newest cfg.Keep and
//     delete the sandbox;
//   - a run failed: once an interval has passed, delete the sandbox and
//     start again;
//   - otherwise: run the warm recipe in the sandbox, creating it if need
//     be, or follow the run already there, and snapshot its disk once
//     the run succeeds.
//
// The watch's warmworkspace pass calls it whenever the repository has no
// fresh snapshot, or has a warm sandbox. user is the account whose token
// clones; empty, the command's own.
func warmWorkspace(ctx context.Context, repoURL, user string, cfg config.WarmWorkspaceConfig) error {
	interval, err := cfg.Validate()
	if err != nil {
		return err
	}
	it, err := parseRecipeTarget(repoURL)
	if err != nil {
		return err
	}
	if !it.IsRepo() {
		return fmt.Errorf("%s is not a repository", repoURL)
	}
	kubeClient, err := clients.NewKubernetesClient()
	if err != nil {
		return fmt.Errorf("creating k8s client: %w", err)
	}
	ns := rootFlags.Namespace
	sb, err := factorysandbox.GetWarmSandbox(ctx, kubeClient, ns, it.Repo)
	if err != nil {
		return err
	}
	if sb != nil {
		a := sb.GetAnnotations()
		switch {
		case a[factorysandbox.AnnotationWarmSnapshot] != "":
			_, err := factorysandbox.FinishWarm(ctx, kubeClient, ns, sb, cfg.KeepOrDefault())
			return err
		case a[factorysandbox.AnnotationWarmFailed] != "":
			failed, _ := time.Parse(time.RFC3339, a[factorysandbox.AnnotationWarmFailed])
			if time.Since(failed) < interval {
				return nil
			}
			fmt.Printf("Warm workspace: the last warm failed at %s; starting again.\n", failed.Format(time.RFC3339))
			if err := factorysandbox.DeleteWarmSandbox(ctx, kubeClient, ns, sb.GetName()); err != nil {
				return err
			}
		}
	}

	overrides, provenance, err := warmScript(ctx, cfg)
	if err != nil {
		return err
	}
	sb, err = factorysandbox.EnsureWarmSandbox(ctx, kubeClient, ns, it.Repo, fmt.Sprintf("https://github.com/%s/%s.git", it.Owner, it.Repo), fmt.Sprintf("https://github.com/%s/%s", it.Owner, it.Repo), rootFlags.Image, rootFlags.DiskSize, rootFlags.StorageClass, rootFlags.EphemeralStorage, rootFlags.ResolvedEnvs, user)
	if err != nil {
		return fmt.Errorf("ensuring the warm sandbox: %w", err)
	}
	runName := sb.GetAnnotations()[factorysandbox.AnnotationWarmRun]
	// A watch that ends mid-run leaves the run going; the next one
	// follows it by its run name.
	rootFlags.AbortOnCancel = false
	if err := runRecipe(ctx, recipe.WarmRecipe, repoURL, runName, "", user, false, applyMode{}, overrides, nil); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_ = factorysandbox.SuspendSandbox(ctx, kubeClient, ns, sb.GetName())
		_ = factorysandbox.AnnotateSandbox(ctx, kubeClient, ns, sb.GetName(), map[string]string{factorysandbox.AnnotationWarmFailed: time.Now().UTC().Format(time.RFC3339)})
		return fmt.Errorf("warm run %s: %w (its logs: factory sandbox task logs %s -n %s)", runName, err, sb.GetName(), ns)
	}

	annotations := map[string]string{factorysandbox.AnnotationWarmScript: provenance}
	head, goVersion := warmRunFacts(ctx, sb.GetName(), runName)
	if head != "" {
		annotations[factorysandbox.AnnotationWarmHead] = head
	}
	if goVersion != "" {
		annotations[factorysandbox.AnnotationWarmGoVersion] = goVersion
	}
	if cfg.ScriptPath != "" {
		annotations[factorysandbox.AnnotationWarmScript] = fmt.Sprintf("%s@%s", provenance, head)
	}
	_, err = factorysandbox.SnapshotWarmSandbox(ctx, kubeClient, ns, sb, annotations, warmSnapshotTTLIntervals*interval, time.Now())
	return err
}

// warmScript is the warm recipe's inputs for cfg's script, and where the
// script came from (factorysandbox.AnnotationWarmScript).
func warmScript(ctx context.Context, cfg config.WarmWorkspaceConfig) (map[string]string, string, error) {
	switch {
	case cfg.Script != "":
		b, err := os.ReadFile(cfg.Script)
		if err != nil {
			return nil, "", fmt.Errorf("reading the warm script: %w", err)
		}
		return map[string]string{"script": string(b)}, "inline", nil
	case cfg.ScriptURL != "":
		b, err := downloadWarmScript(ctx, cfg.ScriptURL)
		if err != nil {
			return nil, "", err
		}
		sum := sha256.Sum256(b)
		return map[string]string{"script": string(b)}, fmt.Sprintf("url %s sha256:%s", cfg.ScriptURL, hex.EncodeToString(sum[:])), nil
	default:
		return map[string]string{"script_path": cfg.ScriptPath}, "path " + cfg.ScriptPath, nil
	}
}

// downloadWarmScript fetches a warm script: https only, at most
// warmScriptMaxBytes, and any status but 200 fails.
func downloadWarmScript(ctx context.Context, url string) ([]byte, error) {
	if !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("warm script URL %q is not https", url)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := warmScriptClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading the warm script: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading the warm script from %s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, warmScriptMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("downloading the warm script: %w", err)
	}
	if len(b) > warmScriptMaxBytes {
		return nil, fmt.Errorf("the warm script at %s is over %d bytes", url, warmScriptMaxBytes)
	}
	return b, nil
}

// warmRunFacts are the commit and Go version the warm run recorded in its
// task directory; empty where it recorded none.
func warmRunFacts(ctx context.Context, sandboxName, runName string) (head, goVersion string) {
	sb, err := taskapi.Connect(ctx, rootFlags.Namespace, sandboxName)
	if err != nil {
		return "", ""
	}
	defer sb.Close()
	entries, err := sb.List(ctx)
	if err != nil {
		return "", ""
	}
	for _, e := range entries {
		if e.RunName != runName {
			continue
		}
		if b, err := sb.ReadFile(ctx, e.ID, "warm-head"); err == nil {
			head = strings.TrimSpace(string(b))
		}
		if b, err := sb.ReadFile(ctx, e.ID, "warm-go"); err == nil {
			goVersion = strings.TrimSpace(string(b))
		}
		break
	}
	return head, goVersion
}
