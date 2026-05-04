package main

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"

	configdirv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/configdir/v1alpha1"
	repowatchv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repowatch/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	scheme = runtime.NewScheme()
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(repowatchv1alpha1.AddToScheme(scheme))
	utilruntime.Must(configdirv1alpha1.AddToScheme(scheme))
}

func main() {
	inputDir := flag.String("input-dir", "user-instructions", "Directory containing user instruction JSON files")
	draft := flag.Bool("draft", false, "Inject as a draft file (.gemini/user-instructions.draft.json)")

	// Handle kubeconfig flag collision: check if already defined to avoid panic
	var kubeconfigPtr *string
	if flag.Lookup("kubeconfig") == nil {
		kubeconfigPtr = flag.String("kubeconfig", "", "Path to kubeconfig file")
	}

	flag.Parse()

	var kubeconfigPath string
	if kubeconfigPtr != nil {
		kubeconfigPath = *kubeconfigPtr
	} else if f := flag.Lookup("kubeconfig"); f != nil {
		kubeconfigPath = f.Value.String()
	}

	if kubeconfigPath == "" {
		if home := homedir.HomeDir(); home != "" {
			kubeconfigPath = filepath.Join(home, ".kube", "config")
		}
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		// Fallback to in-cluster config if kubeconfig not found or failed
		config, err = ctrl.GetConfig()
		if err != nil {
			klog.Fatalf("Failed to get kubeconfig: %v", err)
		}
	}

	k8sClient, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		klog.Fatalf("Failed to create k8s client: %v", err)
	}

	ctx := context.Background()

	// 1. List all RepoWatch resources
	// We need to list across all namespaces to match repos.
	// Alternatively, we could walk the input dir and search per namespace if the input dir structure matches.
	// The previous tool generated `user-instructions/<namespace>/<owner>-<repo>.json`.
	// So we can respect the namespace in the directory structure.

	err = filepath.Walk(*inputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(info.Name(), ".json") {
			return nil
		}

		// Expected path: <inputDir>/<namespace>/<owner>-<repo>.json
		relPath, err := filepath.Rel(*inputDir, path)
		if err != nil {
			return err
		}

		parts := strings.Split(relPath, string(os.PathSeparator))
		if len(parts) != 2 {
			klog.Infof("Skipping %s: unexpected directory structure. Expected <namespace>/<filename>", relPath)
			return nil
		}

		namespace := parts[0]
		//filename := parts[1]
		// filename is owner-repo.json. We need to extract owner and repo?
		// Or better, read the JSON content to get the project name if available?
		// The prompt output format is: { "project_name": "owner/repo", ... }

		content, err := os.ReadFile(path)
		if err != nil {
			klog.Infof("Failed to read %s: %v", path, err)
			return nil
		}

		var instruction struct {
			ProjectName string `json:"project_name"`
		}
		if err := json.Unmarshal(content, &instruction); err != nil {
			klog.Infof("Failed to parse JSON in %s: %v", path, err)
			return nil
		}

		repoName := instruction.ProjectName // "owner/repo"
		if repoName == "" {
			// Fallback to filename parsing if JSON doesn't have it
			// This is risky if repo name has hyphens.
			klog.Infof("Warning: project_name empty in %s. Using matching logic might be flaky.", path)
			// Try to find matching RepoWatch in the namespace
		}

		klog.Infof("Processing %s for repo %s in namespace %s", relPath, repoName, namespace)

		// Find RepoWatch
		rwList := &repowatchv1alpha1.RepoWatchList{}
		if err := k8sClient.List(ctx, rwList, client.InNamespace(namespace)); err != nil {
			klog.Infof("Failed to list RepoWatches in %s: %v", namespace, err)
			return nil
		}

		var matchedRW *repowatchv1alpha1.RepoWatch
		for _, rw := range rwList.Items {
			// Check if RepoURL ends with repoName
			// RepoURL: https://github.com/owner/repo
			// repoName: owner/repo
			if strings.HasSuffix(strings.TrimSuffix(rw.Spec.RepoURL, ".git"), repoName) {
				matchedRW = &rw
				break
			}
		}

		if matchedRW == nil {
			klog.Infof("No RepoWatch found for %s in namespace %s", repoName, namespace)
			return nil
		}

		configDirName := matchedRW.Spec.Review.LLM.ConfigdirRef
		if configDirName == "" {
			klog.Infof("RepoWatch %s has no ConfigDirRef", matchedRW.Name)
			return nil
		}

		// Get ConfigDir
		configDir := &configdirv1alpha1.ConfigDir{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Name: configDirName, Namespace: namespace}, configDir); err != nil {
			klog.Infof("Failed to get ConfigDir %s: %v", configDirName, err)
			return nil
		}

		// Update ConfigDir
		targetPath := ".gemini/user-instructions.json"
		if *draft {
			targetPath = ".gemini/user-instructions.draft.json"
		}

		updated := false
		newFileItem := configdirv1alpha1.FileItem{
			Path: targetPath,
			Source: configdirv1alpha1.FileSource{
				Inline: string(content),
			},
		}

		// Check if file exists and needs update
		found := false
		for i, f := range configDir.Spec.Files {
			if f.Path == targetPath {
				found = true
				if f.Source.Inline != newFileItem.Source.Inline {
					configDir.Spec.Files[i] = newFileItem
					updated = true
				} else {
					klog.Infof("ConfigDir %s already up to date for %s", configDirName, targetPath)
				}
				break
			}
		}

		if !found {
			configDir.Spec.Files = append(configDir.Spec.Files, newFileItem)
			updated = true
		}

		// If we are promoting (not draft), clean up the draft file if it exists
		if !*draft {
			draftPath := ".gemini/user-instructions.draft.json"
			newFiles := []configdirv1alpha1.FileItem{}
			for _, f := range configDir.Spec.Files {
				if f.Path == draftPath {
					klog.Infof("Removing draft file %s from ConfigDir %s", draftPath, configDirName)
					updated = true
					continue
				}
				newFiles = append(newFiles, f)
			}
			configDir.Spec.Files = newFiles
		}

		if updated {
			if err := k8sClient.Update(ctx, configDir); err != nil {
				klog.Infof("Failed to update ConfigDir %s: %v", configDirName, err)
			} else {
				klog.Infof("Updated ConfigDir %s with instructions at %s", configDirName, targetPath)
			}
		}

		return nil
	})

	if err != nil {
		klog.Fatalf("Walk failed: %v", err)
	}
}
