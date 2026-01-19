package store

import (
	"context"
	"fmt"
	"strconv"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/models"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
)

// K8sStore implements the Store interface using Kubernetes resources
type K8sStore struct {
	k8s *k8s.Manager
}

// NewK8sStore creates a new K8sStore
func NewK8sStore(manager *k8s.Manager) *K8sStore {
	return &K8sStore{k8s: manager}
}

func (s *K8sStore) RequiresPopulate() bool {
	return false
}

func (s *K8sStore) SaveRepo(_ context.Context, _, _, _ string) error {
	return nil
}

func (s *K8sStore) DeleteRepo(_ context.Context, _, _ string) error {
	return nil
}

func (s *K8sStore) ListRepos(ctx context.Context, namespace string) ([]string, error) {
	gvr := schema.GroupVersionResource{
		Group:    "review.gemini.google.com",
		Version:  "v1alpha1",
		Resource: "repowatches",
	}
	list, err := s.k8s.Client.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	var names []string
	for _, item := range list.Items {
		names = append(names, item.GetName())
	}
	return names, nil
}

func (s *K8sStore) ListIssues(ctx context.Context, namespace, repo, handler string) ([]models.Issue, error) {
	rw, err := s.k8s.GetRepoWatch(ctx, namespace, repo)
	if err != nil {
		return nil, err
	}

	status, found, err := unstructured.NestedMap(rw.Object, "status")
	if err != nil || !found {
		return []models.Issue{}, nil
	}

	// Parse status to get issue sandboxes for the handler
	issueSandboxesMap, found, _ := unstructured.NestedMap(status, "issueSandboxes")
	if !found {
		issueSandboxesMap = make(map[string]interface{})
	}

	pendingIssuesMap, found, _ := unstructured.NestedMap(status, "pendingIssues")
	if !found {
		pendingIssuesMap = make(map[string]interface{})
	}

	var issues []models.Issue

	// Process active sandboxes
	if handlerSandboxes, ok := issueSandboxesMap[handler]; ok {
		if sandboxesList, ok := handlerSandboxes.([]interface{}); ok {
			for _, sb := range sandboxesList {
				sbMap, ok := sb.(map[string]interface{})
				if !ok {
					continue
				}

				// Safely extract fields
				issueNum := int64(0)
				if val, ok := sbMap["number"].(int64); ok {
					issueNum = val
				} else if val, ok := sbMap["number"].(float64); ok { // JSON unmarshaling often uses float64
					issueNum = int64(val)
				}

				sandboxName, _ := sbMap["sandboxName"].(string)
				// status, _ := sbMap["status"].(string)

				// Fetch IssueSandbox CR for details
				isb, err := s.getIssueSandbox(ctx, namespace, sandboxName)
				if err != nil {
					klog.V(4).Info("Failed to get IssueSandbox", "name", sandboxName, "err", err)
					// Add minimal info
					issues = append(issues, models.Issue{
						ID:         strconv.FormatInt(issueNum, 10),
						AgentState: "Missing",
					})
					continue
				}

				issues = append(issues, s.issueFromSandbox(isb, strconv.FormatInt(issueNum, 10)))
			}
		}
	}

	// Process pending issues
	if handlerPending, ok := pendingIssuesMap[handler]; ok {
		if pendingList, ok := handlerPending.([]interface{}); ok {
			for _, p := range pendingList {
				issueNum := int64(0)
				if val, ok := p.(int64); ok {
					issueNum = val
				} else if val, ok := p.(float64); ok {
					issueNum = int64(val)
				}

				issues = append(issues, models.Issue{
					ID:         strconv.FormatInt(issueNum, 10),
					AgentState: "Pending",
				})
			}
		}
	}

	return issues, nil
}

func (s *K8sStore) SaveIssue(_ context.Context, _, _, _ string, _ models.Issue) error {
	return nil
}

func (s *K8sStore) GetIssue(ctx context.Context, namespace, repo, handler, issueID string) (*models.Issue, error) {
	sandboxName := fmt.Sprintf("%s-issue-%s-%s", repo, issueID, handler)
	isb, err := s.getIssueSandbox(ctx, namespace, sandboxName)
	if err != nil {
		return nil, err
	}
	issue := s.issueFromSandbox(isb, issueID)
	return &issue, nil
}

func (s *K8sStore) UpdateIssueDraft(ctx context.Context, namespace, repo, handler, issueID, draft string) error {
	sandboxName := fmt.Sprintf("%s-issue-%s-%s", repo, issueID, handler)
	return s.k8s.UpdateDevSandboxAnnotation(ctx, namespace, sandboxName, "userDraft", draft)
}

func (s *K8sStore) SaveIssueFeedback(ctx context.Context, namespace, _, repo, handler, issueID, draft, agentDraft, prompt, configdir string) error {
	// Currently Redis store saves this to a separate key "hf:...".
	// For K8s, we could store it in annotations or a Secret.
	// Annotations have size limits (256KB). It might be enough.
	sandboxName := fmt.Sprintf("%s-issue-%s-%s", repo, issueID, handler)

	updates := map[string]string{
		"userDraft":     draft,
		"agentDraft":    agentDraft,
		"prompt":        prompt,
		"configdirname": configdir,
	}

	for k, v := range updates {
		if err := s.k8s.UpdateDevSandboxAnnotation(ctx, namespace, sandboxName, k, v); err != nil {
			return err
		}
	}
	return nil
}

func (s *K8sStore) UpdateIssueComment(ctx context.Context, namespace, repo, handler, issueID, comment string) error {
	sandboxName := fmt.Sprintf("%s-issue-%s-%s", repo, issueID, handler)
	if err := s.k8s.UpdateDevSandboxAnnotation(ctx, namespace, sandboxName, "comment", comment); err != nil {
		return err
	}
	return s.k8s.UpdateDevSandboxAnnotation(ctx, namespace, sandboxName, "userDraft", "")
}

func (s *K8sStore) DeleteIssue(_ context.Context, _, _, _, _ string) error {
	return nil
}

func (s *K8sStore) ListDevSandboxes(ctx context.Context, namespace, repo string) ([]models.DevSandbox, error) {
	// List RepoWatch to get DevSandboxes list
	rw, err := s.k8s.GetRepoWatch(ctx, namespace, repo)
	if err != nil {
		return nil, err
	}

	status, found, err := unstructured.NestedMap(rw.Object, "status")
	if err != nil || !found {
		return []models.DevSandbox{}, nil
	}

	devSandboxesList, found, _ := unstructured.NestedSlice(status, "devSandboxes")
	if !found {
		return []models.DevSandbox{}, nil
	}

	var sandboxes []models.DevSandbox
	for _, item := range devSandboxesList {
		sbMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		sandboxName, _ := sbMap["sandboxName"].(string)
		branchName, _ := sbMap["branchName"].(string)

		isb, err := s.getIssueSandbox(ctx, namespace, sandboxName)
		if err != nil {
			sandboxes = append(sandboxes, models.DevSandbox{
				Name:       sandboxName,
				Branch:     branchName,
				AgentState: "Missing",
			})
			continue
		}

		sb := s.devSandboxFromIssueSandbox(isb)
		sb.Branch = branchName // Ensure branch name is set from RepoWatch status if needed, though IssueSandbox has it too
		sandboxes = append(sandboxes, sb)
	}

	return sandboxes, nil
}

func (s *K8sStore) SaveDevSandbox(_ context.Context, _, _ string, _ models.DevSandbox) error {
	return nil
}

func (s *K8sStore) DeleteDevSandbox(_ context.Context, _, _, _ string) error {
	return nil
}

func (s *K8sStore) ListPRs(ctx context.Context, namespace, repo string) ([]models.PR, error) {
	rw, err := s.k8s.GetRepoWatch(ctx, namespace, repo)
	if err != nil {
		return nil, err
	}

	status, found, err := unstructured.NestedMap(rw.Object, "status")
	if err != nil || !found {
		return []models.PR{}, nil
	}

	reviewSandboxesList, found, _ := unstructured.NestedSlice(status, "reviewSandboxes")
	if !found {
		reviewSandboxesList = []interface{}{}
	}

	pendingPRsList, found, _ := unstructured.NestedSlice(status, "pendingPRs")
	if !found {
		pendingPRsList = []interface{}{}
	}

	var prs []models.PR

	for _, item := range reviewSandboxesList {
		sbMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		prNum := int64(0)
		if val, ok := sbMap["number"].(int64); ok {
			prNum = val
		} else if val, ok := sbMap["number"].(float64); ok {
			prNum = int64(val)
		}

		sandboxName, _ := sbMap["sandboxName"].(string)

		rsb, err := s.getReviewSandbox(ctx, namespace, sandboxName)
		if err != nil {
			prs = append(prs, models.PR{
				ID:         strconv.FormatInt(prNum, 10),
				AgentState: "Missing",
			})
			continue
		}

		prs = append(prs, s.prFromSandbox(rsb, strconv.FormatInt(prNum, 10)))
	}

	for _, item := range pendingPRsList {
		prNum := int64(0)
		if val, ok := item.(int64); ok {
			prNum = val
		} else if val, ok := item.(float64); ok {
			prNum = int64(val)
		}
		prs = append(prs, models.PR{
			ID:         strconv.FormatInt(prNum, 10),
			AgentState: "Pending",
		})
	}

	return prs, nil
}

func (s *K8sStore) SavePR(_ context.Context, _, _ string, _ models.PR) error {
	return nil
}

func (s *K8sStore) GetPR(ctx context.Context, namespace, repo, prID string) (*models.PR, error) {
	sandboxName := fmt.Sprintf("%s-pr-%s", repo, prID)
	rsb, err := s.getReviewSandbox(ctx, namespace, sandboxName)
	if err != nil {
		return nil, err
	}
	pr := s.prFromSandbox(rsb, prID)
	return &pr, nil
}

func (s *K8sStore) UpdatePRDraft(ctx context.Context, namespace, repo, prID, draft string) error {
	sandboxName := fmt.Sprintf("%s-pr-%s", repo, prID)
	return s.k8s.UpdateReviewSandboxUserDraft(ctx, namespace, sandboxName, draft)
}

func (s *K8sStore) UpdatePRReview(ctx context.Context, namespace, repo, prID, review string) error {
	sandboxName := fmt.Sprintf("%s-pr-%s", repo, prID)
	if err := s.k8s.UpdateReviewSandboxAnnotation(ctx, namespace, sandboxName, "review", review); err != nil {
		return err
	}
	return s.k8s.UpdateReviewSandboxUserDraft(ctx, namespace, sandboxName, "")
}

func (s *K8sStore) SavePRFeedback(ctx context.Context, namespace, _, repo, prID, draft, agentDraft, prompt, configdir string) error {
	sandboxName := fmt.Sprintf("%s-pr-%s", repo, prID)
	updates := map[string]string{
		"userDraft":  draft,
		"agentDraft": agentDraft,
		"prompt":     prompt,
		"configdir":  configdir,
	}
	for k, v := range updates {
		if err := s.k8s.UpdateReviewSandboxAnnotation(ctx, namespace, sandboxName, k, v); err != nil {
			return err
		}
	}
	return nil
}

func (s *K8sStore) DeletePR(_ context.Context, _, _, _ string) error {
	return nil
}

// Helpers

func (s *K8sStore) getIssueSandbox(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error) {
	gvr := schema.GroupVersionResource{Group: "custom.agents.x-k8s.io", Version: "v1alpha1", Resource: "issuesandboxes"}
	return s.k8s.Client.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
}

func (s *K8sStore) getReviewSandbox(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error) {
	gvr := schema.GroupVersionResource{Group: "custom.agents.x-k8s.io", Version: "v1alpha1", Resource: "reviewsandboxes"}
	return s.k8s.Client.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
}

func (s *K8sStore) issueFromSandbox(sb *unstructured.Unstructured, id string) models.Issue {
	annotations := sb.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}

	title, _, _ := unstructured.NestedString(sb.Object, "spec", "source", "title")
	htmlURL, _, _ := unstructured.NestedString(sb.Object, "spec", "source", "htmlURL")

	replicas, _, _ := unstructured.NestedInt64(sb.Object, "spec", "replicas")
	sandboxReplica := "0"
	if replicas > 0 {
		sandboxReplica = "1"
	}

	pushBranch := false
	if pb, found, _ := unstructured.NestedBool(sb.Object, "spec", "destination", "pushEnabled"); found {
		pushBranch = pb
	}

	return models.Issue{
		ID:                id,
		Title:             title,
		HTMLURL:           htmlURL,
		Sandbox:           sb.GetName(),
		SandboxReplica:    sandboxReplica,
		AgentState:        annotations["agentState"],
		AgentStateMessage: annotations["agentStateMessage"],
		Draft:             annotations["userDraft"],
		AgentDraft:        annotations["agentDraft"],
		Comment:           annotations["comment"],
		PushBranch:        pushBranch,
		// Labels: ... (Need to decide if we parse k8s labels or what)
	}
}

func (s *K8sStore) devSandboxFromIssueSandbox(sb *unstructured.Unstructured) models.DevSandbox {
	annotations := sb.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}

	replicas, _, _ := unstructured.NestedInt64(sb.Object, "spec", "replicas")
	sandboxReplica := "0"
	if replicas > 0 {
		sandboxReplica = "1"
	}

	branch, _, _ := unstructured.NestedString(sb.Object, "spec", "destination", "branch")
	branchURL := "" // construct from source?

	return models.DevSandbox{
		Name:              sb.GetName(),
		Sandbox:           sb.GetName(),
		SandboxReplica:    sandboxReplica,
		Branch:            branch,
		BranchURL:         branchURL,
		AgentState:        annotations["agentState"],
		AgentStateMessage: annotations["agentStateMessage"],
	}
}

func (s *K8sStore) prFromSandbox(sb *unstructured.Unstructured, id string) models.PR {
	annotations := sb.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}

	title, _, _ := unstructured.NestedString(sb.Object, "spec", "source", "title")
	htmlURL, _, _ := unstructured.NestedString(sb.Object, "spec", "source", "htmlURL")
	diffURL, _, _ := unstructured.NestedString(sb.Object, "spec", "source", "diffURL")

	replicas, _, _ := unstructured.NestedInt64(sb.Object, "spec", "replicas")
	sandboxReplica := "0"
	if replicas > 0 {
		sandboxReplica = "1"
	}

	return models.PR{
		ID:                id,
		Title:             title,
		HTMLURL:           htmlURL,
		DiffURL:           diffURL,
		Sandbox:           sb.GetName(),
		SandboxReplica:    sandboxReplica,
		AgentState:        annotations["agentState"],
		AgentStateMessage: annotations["agentStateMessage"],
		Draft:             annotations["userDraft"],
		AgentDraft:        annotations["agentDraft"],
		Review:            annotations["review"],
		ReviewState:       annotations["reviewState"],
	}
}
