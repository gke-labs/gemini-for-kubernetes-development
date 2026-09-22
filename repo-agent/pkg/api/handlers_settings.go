package api

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

// GcpSecretName holds the member's BYO deploy target (project/region) —
// deliberately not credentials; Workload Identity carries those.
const GcpSecretName = "gcp-config"

// clusterWorkloadIdentity resolves the cluster's WI pool facts from the
// GKE metadata server, once. Off GKE (local dev) both come back empty
// and the settings response simply omits the principal.
var clusterWI struct {
	once      sync.Once
	projectID string
	projectNo string
}

func metadataValue(path string) string {
	client := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequest("GET", "http://metadata.google.internal/computeMetadata/v1/"+path, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return strings.TrimSpace(string(b))
}

// workloadIdentityPrincipalFor is the direct-WI principal of any KSA in
// this cluster — the identity a member grants roles to in their own
// project (deployer for deploys, the controller for secret sync).
func workloadIdentityPrincipalFor(namespace, ksa string) string {
	clusterWI.once.Do(func() {
		clusterWI.projectID = metadataValue("project/project-id")
		clusterWI.projectNo = metadataValue("project/numeric-project-id")
	})
	if clusterWI.projectID == "" || clusterWI.projectNo == "" {
		return ""
	}
	return fmt.Sprintf("principal://iam.googleapis.com/projects/%s/locations/global/workloadIdentityPools/%s.svc.id.goog/subject/ns/%s/sa/%s",
		clusterWI.projectNo, clusterWI.projectID, namespace, ksa)
}

func workloadIdentityPrincipal(namespace string) string {
	return workloadIdentityPrincipalFor(namespace, "factory-deployer")
}

func (s *Server) getSettings(c *gin.Context) {
	namespace := s.Auth.GetNamespaceFromContext(c)
	settings := gin.H{
		"manual_pat_set":        false,
		"oauth_pat_set":         false,
		"gemini_api_key_set":    false,
		"anthropic_api_key_set": false,
		"github_pat_set":        false, // Legacy field for UI compatibility
	}

	if sec, err := s.K8sManager.Clientset.CoreV1().Secrets(namespace).Get(c.Request.Context(), k8s.GithubSecretName, v1.GetOptions{}); err == nil {
		if _, ok := sec.Data[k8s.ManualPATKey]; ok {
			settings["manual_pat_set"] = true
			settings["github_pat_set"] = true
		}
		if _, ok := sec.Data[k8s.OAuthPATKey]; ok {
			settings["oauth_pat_set"] = true
			if !settings["manual_pat_set"].(bool) {
				settings["github_pat_set"] = true
			}
		}
		// Fallback for legacy 'pat' key if neither of the new ones are set
		if !settings["manual_pat_set"].(bool) && !settings["oauth_pat_set"].(bool) {
			if _, ok := sec.Data["pat"]; ok {
				settings["github_pat_set"] = true
			}
		}
	}
	if sec, err := s.K8sManager.Clientset.CoreV1().Secrets(namespace).Get(c.Request.Context(), k8s.GeminiSecretName, v1.GetOptions{}); err == nil {
		if val, ok := sec.Data["gemini"]; ok && len(val) > 0 {
			settings["gemini_api_key_set"] = true
		}
	}
	if sec, err := s.K8sManager.Clientset.CoreV1().Secrets(namespace).Get(c.Request.Context(), k8s.ClaudeSecretName, v1.GetOptions{}); err == nil {
		if val, ok := sec.Data["claude"]; ok && len(val) > 0 {
			settings["anthropic_api_key_set"] = true
		}
	}
	// GCP deploy target is not sensitive — echo the values so the form
	// shows what is set, and hand the UI the member's WI principal plus
	// the grant command they run in their own project.
	if sec, err := s.K8sManager.Clientset.CoreV1().Secrets(namespace).Get(c.Request.Context(), GcpSecretName, v1.GetOptions{}); err == nil {
		settings["gcp_project"] = string(sec.Data["project"])
		settings["gcp_region"] = string(sec.Data["region"])
	}
	// Secret Manager reference mode: references are echoed (they are
	// resource names, not secrets) alongside the controller principal
	// the member grants secretAccessor to.
	refFor := func(secretName, key string) string {
		if sec, err := s.K8sManager.Clientset.CoreV1().Secrets(namespace).Get(c.Request.Context(), secretName, v1.GetOptions{}); err == nil {
			return string(sec.Data[key])
		}
		return ""
	}
	settings["github_pat_ref"] = refFor(k8s.GithubSecretName, "pat-ref")
	settings["gemini_api_key_ref"] = refFor(k8s.GeminiSecretName, "gemini-ref")
	settings["anthropic_api_key_ref"] = refFor(k8s.ClaudeSecretName, "claude-ref")
	if syncPrincipal := workloadIdentityPrincipalFor("repo-agent-system", "repowatch-controller"); syncPrincipal != "" {
		settings["gsm_sync_principal"] = syncPrincipal
		settings["gsm_grant_example"] = fmt.Sprintf(
			"gcloud secrets add-iam-policy-binding SECRET_NAME --project YOUR_PROJECT \\\n  --member %q \\\n  --role roles/secretmanager.secretAccessor",
			syncPrincipal)
	}
	if principal := workloadIdentityPrincipal(namespace); principal != "" {
		settings["gcp_wi_principal"] = principal
		project := "YOUR_PROJECT_ID"
		if p, ok := settings["gcp_project"].(string); ok && p != "" {
			project = p
		}
		settings["gcp_grant_command"] = fmt.Sprintf(
			"gcloud projects add-iam-policy-binding %s \\\n  --member %q \\\n  --role roles/editor --condition None",
			project, principal)
	}
	c.JSON(http.StatusOK, settings)
}

func (s *Server) updateSettings(c *gin.Context) {
	namespace := s.Auth.GetNamespaceFromContext(c)
	var payload struct {
		GithubPAT       *string `json:"github_pat"`        // Use pointer to distinguish between empty string and missing field
		GeminiAPIKey    *string `json:"gemini_api_key"`    // Use pointer to distinguish between empty string and missing field
		AnthropicAPIKey *string `json:"anthropic_api_key"` // Use pointer to distinguish between empty string and missing field
		GcpProject      *string `json:"gcp_project"`
		GcpRegion       *string `json:"gcp_region"`
		GithubPATRef    *string `json:"github_pat_ref"`
		GeminiKeyRef    *string `json:"gemini_api_key_ref"`
		AnthropicKeyRef *string `json:"anthropic_api_key_ref"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if payload.GithubPAT != nil {
		patValue := strings.TrimSpace(*payload.GithubPAT)
		if patValue == "" {
			// Clear manual PAT
			data := map[string][]byte{
				k8s.ManualPATKey: nil,
			}
			err := s.K8sManager.UpdateSecret(c.Request.Context(), namespace, k8s.GithubSecretName, data, nil)
			if err != nil {
				klog.Errorf("Failed to clear GitHub PAT: %v", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to clear GitHub PAT"})
				return
			}
		} else {
			// Set manual PAT
			data := map[string][]byte{
				k8s.ManualPATKey: []byte(patValue),
				"refresh_token":  nil,
				"expiry":         nil,
			}
			err := s.K8sManager.UpdateSecret(c.Request.Context(), namespace, k8s.GithubSecretName, data, nil)
			if err != nil {
				klog.Errorf("Failed to update GitHub PAT: %v", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update GitHub PAT"})
				return
			}
		}
	}

	if payload.GeminiAPIKey != nil {
		geminiValue := strings.TrimSpace(*payload.GeminiAPIKey)
		var data map[string][]byte
		if geminiValue == "" {
			data = map[string][]byte{"gemini": nil}
		} else {
			data = map[string][]byte{"gemini": []byte(geminiValue)}
		}
		err := s.K8sManager.UpdateSecret(c.Request.Context(), namespace, k8s.GeminiSecretName, data, nil)
		if err != nil {
			klog.Errorf("Failed to update Gemini API Key: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update Gemini API Key"})
			return
		}
	}

	if payload.AnthropicAPIKey != nil {
		anthropicValue := strings.TrimSpace(*payload.AnthropicAPIKey)
		var data map[string][]byte
		if anthropicValue == "" {
			data = map[string][]byte{"claude": nil}
		} else {
			data = map[string][]byte{"claude": []byte(anthropicValue)}
		}
		err := s.K8sManager.UpdateSecret(c.Request.Context(), namespace, k8s.ClaudeSecretName, data, nil)
		if err != nil {
			klog.Errorf("Failed to update Anthropic API Key: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update Anthropic API Key"})
			return
		}
	}

	setRef := func(secretName, key string, val *string) bool {
		if val == nil {
			return true
		}
		v := strings.TrimSpace(*val)
		data := map[string][]byte{key: []byte(v)}
		if v == "" {
			data[key] = nil
		}
		if err := s.K8sManager.UpdateSecret(c.Request.Context(), namespace, secretName, data, nil); err != nil {
			klog.Errorf("Failed to update %s/%s reference: %v", secretName, key, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update secret reference"})
			return false
		}
		return true
	}
	if !setRef(k8s.GithubSecretName, "pat-ref", payload.GithubPATRef) ||
		!setRef(k8s.GeminiSecretName, "gemini-ref", payload.GeminiKeyRef) ||
		!setRef(k8s.ClaudeSecretName, "claude-ref", payload.AnthropicKeyRef) {
		return
	}

	if payload.GcpProject != nil || payload.GcpRegion != nil {
		data := map[string][]byte{}
		if payload.GcpProject != nil {
			if v := strings.TrimSpace(*payload.GcpProject); v == "" {
				data["project"] = nil
			} else {
				data["project"] = []byte(v)
			}
		}
		if payload.GcpRegion != nil {
			if v := strings.TrimSpace(*payload.GcpRegion); v == "" {
				data["region"] = nil
			} else {
				data["region"] = []byte(v)
			}
		}
		if err := s.K8sManager.UpdateSecret(c.Request.Context(), namespace, GcpSecretName, data, nil); err != nil {
			klog.Errorf("Failed to update GCP settings: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update GCP settings"})
			return
		}
	}

	c.Status(http.StatusOK)
}
