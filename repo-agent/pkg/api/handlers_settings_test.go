package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/auth"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
)

func TestSettingsHandlers(t *testing.T) {
	k8sClient := kubernetesfake.NewClientset()
	manager := &k8s.Manager{
		Clientset: k8sClient,
	}

	server := &Server{
		K8sManager: manager,
		Auth: &auth.Authenticator{
			K8sManager: manager,
		},
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()

	// Mock Auth middleware
	r.Use(func(c *gin.Context) {
		c.Set(auth.UserKey, "default")
		c.Next()
	})

	r.GET("/settings", server.getSettings)
	r.POST("/settings", server.updateSettings)

	t.Run("Get initial settings (all empty)", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "/settings", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d", w.Code)
		}

		var settings map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &settings)

		expected := map[string]interface{}{
			"manual_pat_set":     false,
			"oauth_pat_set":      false,
			"gemini_api_key_set": false,
			"claude_api_key_set": false,
			"github_pat_set":     false,
		}

		for k, v := range expected {
			if settings[k] != v {
				t.Errorf("Expected %s to be %v, got %v", k, v, settings[k])
			}
		}
	})

	t.Run("Update and get settings", func(t *testing.T) {
		payload := map[string]string{
			"github_pat":      "test-github-pat",
			"gemini_api_key":  "test-gemini-key",
			"claude_api_key":  "test-claude-key",
		}
		jsonValue, _ := json.Marshal(payload)
		req, _ := http.NewRequest("POST", "/settings", bytes.NewBuffer(jsonValue))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d", w.Code)
		}

		// Verify secrets were created in K8s
		ctx := context.Background()
		if sec, err := k8sClient.CoreV1().Secrets("default").Get(ctx, k8s.GithubSecretName, metav1.GetOptions{}); err != nil {
			t.Errorf("Github secret not found: %v", err)
		} else if string(sec.Data[k8s.ManualPATKey]) != "test-github-pat" {
			t.Errorf("Expected github pat 'test-github-pat', got %s", string(sec.Data[k8s.ManualPATKey]))
		}

		if sec, err := k8sClient.CoreV1().Secrets("default").Get(ctx, k8s.GeminiSecretName, metav1.GetOptions{}); err != nil {
			t.Errorf("Gemini secret not found: %v", err)
		} else if string(sec.Data["gemini"]) != "test-gemini-key" {
			t.Errorf("Expected gemini key 'test-gemini-key', got %s", string(sec.Data["gemini"]))
		}

		if sec, err := k8sClient.CoreV1().Secrets("default").Get(ctx, k8s.ClaudeSecretName, metav1.GetOptions{}); err != nil {
			t.Errorf("Claude secret not found: %v", err)
		} else if string(sec.Data["claude"]) != "test-claude-key" {
			t.Errorf("Expected claude key 'test-claude-key', got %s", string(sec.Data["claude"]))
		}

		// Get settings again and verify status
		req, _ = http.NewRequest("GET", "/settings", nil)
		w = httptest.NewRecorder()
		r.ServeHTTP(w, req)

		var settings map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &settings)

		expected := map[string]interface{}{
			"manual_pat_set":     true,
			"gemini_api_key_set": true,
			"claude_api_key_set": true,
			"github_pat_set":     true,
		}

		for k, v := range expected {
			if settings[k] != v {
				t.Errorf("Expected %s to be %v, got %v", k, v, settings[k])
			}
		}
	})

	t.Run("Clear Github PAT", func(t *testing.T) {
		githubPat := ""
		payload := map[string]*string{
			"github_pat": &githubPat,
		}
		jsonValue, _ := json.Marshal(payload)
		req, _ := http.NewRequest("POST", "/settings", bytes.NewBuffer(jsonValue))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d", w.Code)
		}

		// Verify secret was updated in K8s
		ctx := context.Background()
		if sec, err := k8sClient.CoreV1().Secrets("default").Get(ctx, k8s.GithubSecretName, metav1.GetOptions{}); err != nil {
			t.Errorf("Github secret not found: %v", err)
		} else if _, ok := sec.Data[k8s.ManualPATKey]; ok {
			t.Errorf("Expected github pat to be cleared, but it still exists")
		}
	})

    t.Run("OAuth PAT set", func(t *testing.T) {
        // Manually create a secret with oauth_pat
        ctx := context.Background()
        k8sClient.CoreV1().Secrets("default").Delete(ctx, k8s.GithubSecretName, metav1.DeleteOptions{})
        secret := &corev1.Secret{
            ObjectMeta: metav1.ObjectMeta{
                Name: k8s.GithubSecretName,
                Namespace: "default",
            },
            Data: map[string][]byte{
                k8s.OAuthPATKey: []byte("oauth-token"),
            },
        }
        k8sClient.CoreV1().Secrets("default").Create(ctx, secret, metav1.CreateOptions{})

        req, _ := http.NewRequest("GET", "/settings", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		var settings map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &settings)

        if settings["oauth_pat_set"] != true {
            t.Errorf("Expected oauth_pat_set to be true")
        }
        if settings["github_pat_set"] != true {
            t.Errorf("Expected github_pat_set (legacy) to be true")
        }
        if settings["manual_pat_set"] != false {
            t.Errorf("Expected manual_pat_set to be false")
        }
    })
}
