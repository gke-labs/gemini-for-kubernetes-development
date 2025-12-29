package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	pkgk8s "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/review-ui/review-api/pkg/k8s"
	"github.com/google/go-github/v39/github"
	"golang.org/x/oauth2"
	githuboauth "golang.org/x/oauth2/github"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	UserKey = "ghUser"
)

type Authenticator struct {
	OAuthConfig       *oauth2.Config
	OAuthState        string
	K8sManager        *k8s.Manager
	AllowedUsers      []string
	bootstrappedUsers sync.Map
}

func NewAuthenticator(manager *k8s.Manager, allowedUsers []string) *Authenticator {
	a := &Authenticator{
		K8sManager:   manager,
		AllowedUsers: allowedUsers,
	}
	a.InitOAuth()
	return a
}

func (a *Authenticator) InitOAuth() {
	clientID := os.Getenv("GITHUB_CLIENT_ID")
	clientSecret := os.Getenv("GITHUB_CLIENT_SECRET")

	if clientID == "" || clientSecret == "" {
		log.Println("Warning: GITHUB_CLIENT_ID or GITHUB_CLIENT_SECRET not set. OAuth will not work.")
	}

	a.OAuthConfig = &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes:       []string{"read:user", "user:email"},
		Endpoint:     githuboauth.Endpoint,
	}

	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		log.Fatalf("Failed to generate random OAuth state: %v", err)
	}
	a.OAuthState = base64.URLEncoding.EncodeToString(b)
}

func (a *Authenticator) IsUserAllowed(username string) bool {
	if len(a.AllowedUsers) == 0 {
		return true // No restrictions
	}
	for _, allowed := range a.AllowedUsers {
		if strings.EqualFold(username, allowed) {
			return true
		}
	}
	return false
}

func (a *Authenticator) Login(c *gin.Context) {
	if a.OAuthConfig.ClientID == "" {
		c.String(http.StatusInternalServerError, "GitHub OAuth is not configured. Please set GITHUB_CLIENT_ID and GITHUB_CLIENT_SECRET in the github-token secret.")
		return
	}
	scheme := "http"
	if c.Request.TLS != nil || c.Request.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}

	// Create a local copy of the config to modify scopes per request
	localConf := *a.OAuthConfig
	localConf.RedirectURL = fmt.Sprintf("%s://%s/api/auth/callback", scheme, c.Request.Host)

	scope := c.Query("scope")
	if scope == "readwrite" {
		localConf.Scopes = []string{"repo", "read:user", "user:email"}
	} else {
		// Default to read-only
		localConf.Scopes = []string{"read:user", "user:email"}
	}

	url := localConf.AuthCodeURL(a.OAuthState, oauth2.AccessTypeOnline)
	c.Redirect(http.StatusTemporaryRedirect, url)
}

func (a *Authenticator) Callback(c *gin.Context) {
	if c.Query("state") != a.OAuthState {
		c.String(http.StatusBadRequest, "Invalid OAuth state")
		return
	}
	token, err := a.OAuthConfig.Exchange(c.Request.Context(), c.Query("code"))
	if err != nil {
		log.Printf("OAuth exchange failed: %v", err)
		c.String(http.StatusInternalServerError, "Authentication failed")
		return
	}

	client := github.NewClient(a.OAuthConfig.Client(c.Request.Context(), token))
	user, _, err := client.Users.Get(c.Request.Context(), "")
	if err != nil {
		log.Printf("Failed to get GitHub user: %v", err)
		c.String(http.StatusInternalServerError, "Failed to get user info")
		return
	}

	ghUser := strings.ToLower(user.GetLogin())

	// Enforce allowlist for GitHub users
	if !a.IsUserAllowed(ghUser) {
		log.Printf("Unauthorized GitHub user attempted to log in: %s", ghUser)
		c.String(http.StatusForbidden, "Unauthorized GitHub user. Please contact administrator.")
		return
	}

	if err := pkgk8s.BootstrapNamespace(c.Request.Context(), a.K8sManager.Clientset, ghUser); err != nil {
		log.Printf("Failed to bootstrap namespace %s: %v", ghUser, err)
	}

	// Update the secret with the user's token and info
	if err := a.updateUserSecret(c.Request.Context(), ghUser, token, user); err != nil {
		log.Printf("Failed to update user secret: %v", err)
		c.String(http.StatusInternalServerError, "Failed to update user secret")
		return
	}

	session := sessions.Default(c)
	session.Set(UserKey, ghUser)
	if err := session.Save(); err != nil {
		log.Printf("Failed to save session: %v", err)
		c.String(http.StatusInternalServerError, "Failed to save session")
		return
	}
	c.Redirect(http.StatusTemporaryRedirect, "/")
}

func (a *Authenticator) updateUserSecret(ctx context.Context, namespace string, token *oauth2.Token, user *github.User) error {
	data := map[string][]byte{
		k8s.OAuthPATKey: []byte(token.AccessToken),
	}

	if token.RefreshToken != "" {
		data["refresh_token"] = []byte(token.RefreshToken)
	}
	if !token.Expiry.IsZero() {
		data["expiry"] = []byte(token.Expiry.Format(time.RFC3339))
	}
	if user.Name != nil {
		data["name"] = []byte(*user.Name)
	}
	if user.Email != nil {
		data["email"] = []byte(*user.Email)
	}
	return a.K8sManager.UpdateSecret(ctx, namespace, pkgk8s.GithubSecretName, data, nil)
}

func (a *Authenticator) Status(c *gin.Context) {
	session := sessions.Default(c)
	if user := session.Get(UserKey); user != nil {
		c.JSON(http.StatusOK, gin.H{"authenticated": true, "user": user})
		return
	}
	c.JSON(http.StatusUnauthorized, gin.H{"authenticated": false})
}

func (a *Authenticator) Logout(c *gin.Context) {
	session := sessions.Default(c)
	session.Delete(UserKey)
	if err := session.Save(); err != nil {
		log.Printf("Failed to save session: %v", err)
		c.String(http.StatusInternalServerError, "Failed to save session")
		return
	}
	c.Status(http.StatusOK)
}

func (a *Authenticator) GetProviders(c *gin.Context) {
	configured := a.OAuthConfig.ClientID != "" && a.OAuthConfig.ClientSecret != ""
	c.JSON(http.StatusOK, gin.H{"github": configured})
}

func (a *Authenticator) UpdateGithubConfig(c *gin.Context) {
	var payload struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if payload.ClientID == "" || payload.ClientSecret == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "client_id and client_secret are required"})
		return
	}

	// Update Secret in repo-agent-system
	// We need to get the existing secret to preserve the PAT
	secret, err := a.K8sManager.Clientset.CoreV1().Secrets(pkgk8s.SystemNamespace).Get(c.Request.Context(), pkgk8s.GithubSecretName, v1.GetOptions{})
	if err != nil {
		log.Printf("Failed to get github secret: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get github secret"})
		return
	}

	if secret.Data == nil {
		secret.Data = make(map[string][]byte)
	}
	secret.Data["github-client-id"] = []byte(payload.ClientID)
	secret.Data["github-client-secret"] = []byte(payload.ClientSecret)

	_, err = a.K8sManager.Clientset.CoreV1().Secrets(pkgk8s.SystemNamespace).Update(c.Request.Context(), secret, v1.UpdateOptions{})
	if err != nil {
		log.Printf("Failed to update github secret: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update github secret"})
		return
	}

	// Update in-memory config
	a.OAuthConfig.ClientID = payload.ClientID
	a.OAuthConfig.ClientSecret = payload.ClientSecret

	c.Status(http.StatusOK)
}

func (a *Authenticator) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		session := sessions.Default(c)
		userVal := session.Get(UserKey)

		// If no user is logged in, default to "default" namespace (guest mode)
		// The user requested: "no auth" logic that puts the user in the default namespace
		user := "default"
		if userVal != nil {
			user = userVal.(string)
		}

		// The Auth.Middleware is calling BootstrapNamespace (which makes ~10 Kubernetes API calls) for every single request.
		// When loading the VS Code UI, the browser requests hundreds of static assets (JS, CSS, icons). This triggers
		//  thousands of K8s API calls in seconds, causing the client to throttle itself and eventually timing out the requests
		// (context canceled, 502/504 errors).
		// Use in-memory cache (sync.Map) for the Authenticator.
		// First Request: The middleware checks the cache, finds it empty for the user, calls BootstrapNamespace once, and stores
		//   the result.
		// Subsequent Requests: The middleware finds the user in the cache and skips the K8s operations entirely.
		// dramatically reduce latency and eliminate the 502/504 errors caused by client-side throttling, allowing the VS Code UI to load correctly
		if _, ok := a.bootstrappedUsers.Load(user); !ok {
			// Lazy bootstrap checks if namespace exists, creating it if needed.
			if err := pkgk8s.BootstrapNamespace(c.Request.Context(), a.K8sManager.Clientset, user); err != nil {
				log.Printf("Lazy bootstrap failed for user %s: %v", user, err)
			} else {
				a.bootstrappedUsers.Store(user, true)
			}
		}

		c.Set(UserKey, user)
		c.Next()
	}
}

func (a *Authenticator) GetUserFromContext(c *gin.Context) string {
	val, exists := c.Get(UserKey)
	if !exists {
		return ""
	}
	if user, ok := val.(string); ok {
		return user
	}
	return ""
}
