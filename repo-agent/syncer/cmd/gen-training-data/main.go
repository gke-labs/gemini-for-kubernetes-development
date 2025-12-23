package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"

	"cloud.google.com/go/storage"
	"github.com/google/go-github/v39/github"
	"golang.org/x/oauth2"
	"google.golang.org/api/iterator"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
	outputFile := flag.String("output-file", "training-data.jsonl", "Output JSONL file")
	flag.Parse()

	if *gcsBucket == "" {
		log.Fatal("--gcs-bucket is required")
	}
	if *githubToken == "" {
		log.Fatal("--github-token is required")
	}

	ctx := context.Background()

	// Initialize GCS Client
	gcsClient, err := storage.NewClient(ctx)
	if err != nil {
		log.Fatalf("Failed to create GCS client: %v", err)
	}
	defer gcsClient.Close()

	// Initialize GitHub Client
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: *githubToken},
	)
	tc := oauth2.NewClient(ctx, ts)
	ghClient := github.NewClient(tc)

	// Open Output File
	f, err := os.Create(*outputFile)
	if err != nil {
		log.Fatalf("Failed to create output file: %v", err)
	}
	defer f.Close()

	// Iterate GCS Objects
	it := gcsClient.Bucket(*gcsBucket).Objects(ctx, &storage.Query{Prefix: *gcsPrefix})
	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			log.Fatalf("Failed to list objects: %v", err)
		}

		if !strings.HasSuffix(attrs.Name, ".yaml") {
			continue
		}

		log.Printf("Processing %s", attrs.Name)
		rc, err := gcsClient.Bucket(*gcsBucket).Object(attrs.Name).NewReader(ctx)
		if err != nil {
			log.Printf("Failed to read object %s: %v", attrs.Name, err)
			continue
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			log.Printf("Failed to read content of %s: %v", attrs.Name, err)
			continue
		}

		// Parse Unstructured
		var u unstructured.Unstructured
		if err := yaml.Unmarshal(data, &u.Object); err != nil {
			log.Printf("Failed to unmarshal YAML %s: %v", attrs.Name, err)
			continue
		}

		if u.GetKind() != "ReviewSandbox" {
			continue
		}

		// Extract Agent Draft
		annotations := u.GetAnnotations()
		agentDraftYAML, ok := annotations["agentDraft"]
		if !ok || agentDraftYAML == "" {
			log.Printf("No agentDraft annotation in %s", attrs.Name)
			continue
		}

		var agentOutput AgentOutput
		if err := yaml.Unmarshal([]byte(agentDraftYAML), &agentOutput); err != nil {
			log.Printf("Failed to unmarshal agentDraft in %s: %v", attrs.Name, err)
			continue
		}

		// Extract PR Info
		spec, _, _ := unstructured.NestedMap(u.Object, "spec")
		source, _, _ := unstructured.NestedMap(spec, "source")
		prURL, _, _ := unstructured.NestedString(source, "htmlURL")

		if prURL == "" {
			log.Printf("No htmlURL in source spec for %s", attrs.Name)
			continue
		}

		// URL format: https://github.com/owner/repo/pull/123 or just https://github.com/owner/repo
		parts := strings.Split(strings.TrimPrefix(prURL, "https://github.com/"), "/")
		if len(parts) < 4 || parts[2] != "pull" {
			log.Printf("Invalid PR URL format: %s", prURL)
			continue
		}
		owner := parts[0]
		repo := parts[1]
		prNumber, err := strconv.Atoi(parts[3])
		if err != nil {
			log.Printf("Invalid PR number in URL: %s", prURL)
			continue
		}

		// Fetch PR Status
		pr, _, err := ghClient.PullRequests.Get(ctx, owner, repo, prNumber)
		if err != nil {
			log.Printf("Failed to get PR %s/%s/%d: %v", owner, repo, prNumber, err)
			continue
		}

		// Fetch Reviews
		reviews, _, err := ghClient.PullRequests.ListReviews(ctx, owner, repo, prNumber, nil)
		if err != nil {
			log.Printf("Failed to list reviews for %s/%s/%d: %v", owner, repo, prNumber, err)
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
			log.Printf("Failed to marshal record: %v", err)
			continue
		}
		n, err := f.Write(jsonData)
		if err != nil {
			log.Fatalf("Failed to write record: %v", err)
		}
		if n != len(jsonData) {
			log.Fatalf("Short write for record: %d != %d", n, len(jsonData))
		}
		n, err = f.Write([]byte("\n"))
		if err != nil {
			log.Fatalf("Failed to write record: %v", err)
		}
		if n != len("\n") {
			log.Fatalf("Short write for record: %d != %d", n, len("\n"))
		}
	}
	log.Println("Processing complete.")
}
