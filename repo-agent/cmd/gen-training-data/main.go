package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"cloud.google.com/go/storage"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"google.golang.org/api/iterator"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"
)

// AgentOutput defines the structure for the agent's YAML output.
// We use a loose structure for Review to avoid compilation errors if the
// go-github struct doesn't match our expectation of having Comments.
type AgentOutput struct {
	Note   string      `yaml:"note"`
	Review interface{} `yaml:"review"`
}

type TrainingDataRecord struct {
	PRNumber     int         `json:"pr_number"`
	Repo         string      `json:"repo"`
	PRURL        string      `json:"pr_url"`
	AgentReview  AgentOutput `json:"agent_review_draft"`
	HumanReviews []Review    `json:"human_reviews"`
	State        string      `json:"pr_state"`
}

type Review struct {
	User  string `json:"user"`
	Body  string `json:"body"`
	State string `json:"state"`
}

func main() {
	gcsBucket := flag.String("gcs-bucket", "", "Name of the GCS bucket")
	gcsPrefix := flag.String("gcs-prefix", "", "Prefix for GCS objects (optional)")
	githubToken := flag.String("github-token", os.Getenv("GITHUB_TOKEN"), "GitHub Personal Access Token")
	outputFile := flag.String("output-file", "", "Output JSONL file (legacy single-file mode)")
	outputDir := flag.String("output-dir", "training-data", "Output directory for split files (one per user/repo). Used if output-file is empty.")
	flag.Parse()

	if *gcsBucket == "" {
		klog.Fatal("--gcs-bucket is required")
	}
	if *githubToken == "" {
		klog.Fatal("--github-token is required")
	}

	ctx := context.Background()

	// Initialize GCS Client
	gcsClient, err := storage.NewClient(ctx)
	if err != nil {
		klog.Fatalf("Failed to create GCS client: %v", err)
	}
	defer gcsClient.Close()

	// Initialize GitHub Client
	ghClient := clients.NewGitHubClient(ctx, *githubToken)

	// Determine Output Mode
	var singleFile *os.File
	if *outputFile != "" {
		var err error
		singleFile, err = os.Create(*outputFile)
		if err != nil {
			klog.Fatalf("Failed to create output file: %v", err)
		}
		defer singleFile.Close()
	} else {
		// Ensure output directory exists
		if err := os.MkdirAll(*outputDir, 0755); err != nil {
			klog.Fatalf("Failed to create output directory: %v", err)
		}
	}

	// Track opened files to truncate on first write, then append
	openedFiles := make(map[string]bool)

	// Iterate GCS Objects
	it := gcsClient.Bucket(*gcsBucket).Objects(ctx, &storage.Query{Prefix: *gcsPrefix})
	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			klog.Fatalf("Failed to list objects: %v", err)
		}

		if !strings.HasSuffix(attrs.Name, ".yaml") {
			continue
		}

		klog.Infof("Processing %s", attrs.Name)
		rc, err := gcsClient.Bucket(*gcsBucket).Object(attrs.Name).NewReader(ctx)
		if err != nil {
			klog.Infof("Failed to read object %s: %v", attrs.Name, err)
			continue
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			klog.Infof("Failed to read content of %s: %v", attrs.Name, err)
			continue
		}

		// Parse Unstructured
		var u unstructured.Unstructured
		if err := yaml.Unmarshal(data, &u.Object); err != nil {
			klog.Infof("Failed to unmarshal YAML %s: %v", attrs.Name, err)
			continue
		}

		if u.GetKind() != "ReviewSandbox" && u.GetKind() != "Sandbox" {
			continue
		}

		if u.GetKind() == "Sandbox" {
			labels := u.GetLabels()
			sType := labels["sandbox.gemini.google.com/type"]
			if sType == "" {
				sType = labels["sandbox-type"]
			}
			if sType != "review" {
				continue
			}
		}

		// Extract Agent Draft
		annotations := u.GetAnnotations()
		agentDraftYAML, ok := annotations["agentDraft"]
		if !ok || agentDraftYAML == "" {
			klog.Infof("No agentDraft annotation in %s", attrs.Name)
			continue
		}

		var agentOutput AgentOutput
		if err := yaml.Unmarshal([]byte(agentDraftYAML), &agentOutput); err != nil {
			klog.Infof("Failed to unmarshal agentDraft in %s: %v", attrs.Name, err)
			continue
		}

		// Extract PR Info
		var prURL string
		spec, _, _ := unstructured.NestedMap(u.Object, "spec")
		source, _, _ := unstructured.NestedMap(spec, "source")
		if source != nil {
			prURL, _, _ = unstructured.NestedString(source, "htmlURL")
		}
		if prURL == "" {
			// Fallback to annotations
			prURL = annotations["htmlURL"]
		}

		if prURL == "" {
			klog.Infof("No htmlURL found in %s", attrs.Name)
			continue
		}

		// URL format: https://github.com/owner/repo/pull/123 or just https://github.com/owner/repo
		parts := strings.Split(strings.TrimPrefix(prURL, "https://github.com/"), "/")
		if len(parts) < 4 || parts[2] != "pull" {
			klog.Infof("Invalid PR URL format: %s", prURL)
			continue
		}
		owner := parts[0]
		repo := parts[1]
		prNumber, err := strconv.Atoi(parts[3])
		if err != nil {
			klog.Infof("Invalid PR number in URL: %s", prURL)
			continue
		}

		// Fetch PR Status
		pr, _, err := ghClient.PullRequests.Get(ctx, owner, repo, prNumber)
		if err != nil {
			klog.Infof("Failed to get PR %s/%s/%d: %v", owner, repo, prNumber, err)
			continue
		}

		// Fetch Reviews
		reviews, _, err := ghClient.PullRequests.ListReviews(ctx, owner, repo, prNumber, nil)
		if err != nil {
			klog.Infof("Failed to list reviews for %s/%s/%d: %v", owner, repo, prNumber, err)
			continue
		}

		humanReviews := []Review{}

		for _, r := range reviews {
			humanReviews = append(humanReviews, Review{
				User:  r.User.GetLogin(),
				Body:  r.GetBody(),
				State: r.GetState(),
			})
		}

		record := TrainingDataRecord{
			PRNumber:     prNumber,
			Repo:         fmt.Sprintf("%s/%s", owner, repo),
			PRURL:        prURL,
			AgentReview:  agentOutput,
			HumanReviews: humanReviews,
			State:        pr.GetState(),
		}

		jsonData, err := json.Marshal(record)
		if err != nil {
			klog.Infof("Failed to marshal record: %v", err)
			continue
		}

		// Determine Writer
		var writer io.Writer
		if singleFile != nil {
			writer = singleFile
		} else {
			// Directory Mode: outputDir/namespace/owner-repo.jsonl
			namespace := u.GetNamespace()
			if namespace == "" {
				namespace = "default"
			}
			userDir := filepath.Join(*outputDir, namespace)
			if err := os.MkdirAll(userDir, 0755); err != nil {
				klog.Infof("Failed to create user directory %s: %v", userDir, err)
				continue
			}

			// Filename: owner-repo.jsonl
			fname := fmt.Sprintf("%s-%s.jsonl", owner, repo)
			fpath := filepath.Join(userDir, fname)

			flags := os.O_WRONLY | os.O_CREATE
			if !openedFiles[fpath] {
				flags |= os.O_TRUNC
				openedFiles[fpath] = true
			} else {
				flags |= os.O_APPEND
			}

			f, err := os.OpenFile(fpath, flags, 0644)
			if err != nil {
				klog.Infof("Failed to open file %s: %v", fpath, err)
				continue
			}
			writer = f
		}

		n, err := writer.Write(jsonData)
		if err != nil {
			if singleFile != nil {
				klog.Fatalf("Failed to write record: %v", err)
			}
			klog.Infof("Failed to write record: %v", err)
			writer.(*os.File).Close()
			continue
		}
		if n != len(jsonData) {
			if singleFile != nil {
				klog.Fatalf("Short write for record: %d != %d", n, len(jsonData))
			}
			klog.Infof("Short write for record: %d != %d", n, len(jsonData))
			writer.(*os.File).Close()
			continue
		}

		n, err = writer.Write([]byte("\n"))
		if err != nil {
			if singleFile != nil {
				klog.Fatalf("Failed to write record: %v", err)
			}
			klog.Infof("Failed to write record: %v", err)
			writer.(*os.File).Close()
			continue
		}
		if n != len("\n") {
			if singleFile != nil {
				klog.Fatalf("Short write for record: %d != %d", n, len("\n"))
			}
			klog.Infof("Short write for record: %d != %d", n, len("\n"))
			writer.(*os.File).Close()
			continue
		}

		// Close file if in directory mode
		if singleFile == nil {
			writer.(*os.File).Close()
		}
	}
	klog.Info("Processing complete.")
}
