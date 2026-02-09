package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/google/go-github/v39/github"
	"golang.org/x/oauth2"
	githuboauth "golang.org/x/oauth2/github"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

const (
	UserKey      = "ghUser"
	NamespaceKey = "namespace"
)

type Authenticator struct {
	OAuthConfig       *oauth2.Config
	OAuthState        string
	K8sManager        *k8s.Manager
	AllowedUsers      []string
	AdminUsers        []string
	bootstrappedUsers sync.Map
}

func NewAuthenticator(manager *k8s.Manager, allowedUsers []string, adminUsers []string) *Authenticator {
	a := &Authenticator{
		K8sManager:   manager,
		AllowedUsers: allowedUsers,
		AdminUsers:   adminUsers,
	}
	a.InitOAuth()
	return a
}

func (a *Authenticator) InitOAuth() {
	clientID := os.Getenv("GITHUB_CLIENT_ID")
	clientSecret := os.Getenv("GITHUB_CLIENT_SECRET")

	if clientID == "" || clientSecret == "" {
		klog.Info("Warning: GITHUB_CLIENT_ID or GITHUB_CLIENT_SECRET not set. OAuth will not work.")
	}

	a.OAuthConfig = &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes:       []string{"read:user", "user:email"},
		Endpoint:     githuboauth.Endpoint,
	}

	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		klog.Fatalf("Failed to generate random OAuth state: %v", err)
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

func (a *Authenticator) IsUserAdmin(username string) bool {
	for _, admin := range a.AdminUsers {
		if strings.EqualFold(username, admin) {
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
	log := klog.FromContext(c.Request.Context())
	if c.Query("state") != a.OAuthState {
		c.String(http.StatusBadRequest, "Invalid OAuth state")
		return
	}
	token, err := a.OAuthConfig.Exchange(c.Request.Context(), c.Query("code"))
	if err != nil {
		log.Info("OAuth exchange failed", "err", err)
		c.String(http.StatusInternalServerError, "Authentication failed")
		return
	}

	client := clients.NewGitHubClientFromHTTP(a.OAuthConfig.Client(c.Request.Context(), token))
	user, _, err := client.Users.Get(c.Request.Context(), "")
	if err != nil {
		log.Info("Failed to get GitHub user", "err", err)
		c.String(http.StatusInternalServerError, "Failed to get user info")
		return
	}

	ghUser := strings.ToLower(user.GetLogin())

	// Enforce allowlist for GitHub users
	if !a.IsUserAllowed(ghUser) {
		log.Info("Unauthorized GitHub user attempted to log in", "user", ghUser)
		c.String(http.StatusForbidden, "Unauthorized GitHub user. Please contact administrator.")
		return
	}

	if err := k8s.BootstrapNamespace(c.Request.Context(), a.K8sManager.Clientset, ghUser); err != nil {
		log.Info("Failed to bootstrap namespace", "user", ghUser, "err", err)
	}

	// Update the secret with the user's token and info
	if err := a.updateUserSecret(c.Request.Context(), ghUser, token, user); err != nil {
		log.Info("Failed to update user secret", "err", err)
		c.String(http.StatusInternalServerError, "Failed to update user secret")
		return
	}

	session := sessions.Default(c)
	session.Set(UserKey, ghUser)
	// Reset namespace on new login
	session.Delete(NamespaceKey)
	if err := session.Save(); err != nil {
		log.Info("Failed to save session", "err", err)
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
	return a.K8sManager.UpdateSecret(ctx, namespace, k8s.GithubSecretName, data, nil)
}

func (a *Authenticator) Status(c *gin.Context) {
	session := sessions.Default(c)
	userVal := session.Get(UserKey)
	if userVal != nil {
		user := userVal.(string)
		isAdmin := a.IsUserAdmin(user)
		namespace := user
		if nsVal := session.Get(NamespaceKey); nsVal != nil {
			namespace = nsVal.(string)
		}
		c.JSON(http.StatusOK, gin.H{
			"authenticated": true,
			"user":          user,
			"isAdmin":       isAdmin,
			"namespace":     namespace,
		})
		return
	}
	c.JSON(http.StatusUnauthorized, gin.H{"authenticated": false})
}

func (a *Authenticator) Logout(c *gin.Context) {
	log := klog.FromContext(c.Request.Context())
	session := sessions.Default(c)
	session.Delete(UserKey)
	session.Delete(NamespaceKey)
	if err := session.Save(); err != nil {
		log.Info("Failed to save session", "err", err)
		c.String(http.StatusInternalServerError, "Failed to save session")
		return
	}
	c.Status(http.StatusOK)
}

func (a *Authenticator) SwitchNamespace(c *gin.Context) {
	session := sessions.Default(c)
	userVal := session.Get(UserKey)
	if userVal == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Not authenticated"})
		return
	}
	user := userVal.(string)

	if !a.IsUserAdmin(user) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Only admins can switch namespaces"})
		return
	}

	var payload struct {
		Namespace string `json:"namespace"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if payload.Namespace == "" {
		// Reset to user's own namespace
		session.Delete(NamespaceKey)
	} else {
		session.Set(NamespaceKey, payload.Namespace)
	}

	if err := session.Save(); err != nil {
		klog.Errorf("Failed to save session: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save session"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "success", "namespace": payload.Namespace})
}

func (a *Authenticator) GetProviders(c *gin.Context) {
	configured := a.OAuthConfig.ClientID != "" && a.OAuthConfig.ClientSecret != ""
	c.JSON(http.StatusOK, gin.H{"github": configured})
}

func (a *Authenticator) UpdateGithubConfig(c *gin.Context) {
	log := klog.FromContext(c.Request.Context())
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
	secret, err := a.K8sManager.Clientset.CoreV1().Secrets(k8s.SystemNamespace).Get(c.Request.Context(), k8s.GithubSecretName, v1.GetOptions{})
	if err != nil {
		log.Info("Failed to get github secret", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get github secret"})
		return
	}

	if secret.Data == nil {
		secret.Data = make(map[string][]byte)
	}
	secret.Data["github-client-id"] = []byte(payload.ClientID)
	secret.Data["github-client-secret"] = []byte(payload.ClientSecret)

	_, err = a.K8sManager.Clientset.CoreV1().Secrets(k8s.SystemNamespace).Update(c.Request.Context(), secret, v1.UpdateOptions{})
	if err != nil {
		log.Info("Failed to update github secret", "err", err)
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
		log := klog.FromContext(c.Request.Context())
		session := sessions.Default(c)
		userVal := session.Get(UserKey)

		// If no user is logged in, default to "default" namespace (guest mode)
		// The user requested: "no auth" logic that puts the user in the default namespace
		user := "default"
		if userVal != nil {
			user = userVal.(string)
		}

		// Determine target namespace
		namespace := user
		if nsVal := session.Get(NamespaceKey); nsVal != nil {
			namespace = nsVal.(string)
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

		// Note: We bootstrap the *target* namespace, because that's what we are accessing.
		// If an admin switches to 'bob', we need 'bob' namespace to exist?
		// Actually, if 'bob' doesn't exist, maybe we shouldn't bootstrap it implicitly?
		// But existing logic bootstraps 'user'.
		// If admin switches to a namespace, presumably it exists or they want to create it?
		// Let's stick to bootstrapping the *target* namespace.

		if _, ok := a.bootstrappedUsers.Load(namespace); !ok {
			// Lazy bootstrap checks if namespace exists, creating it if needed.
			if err := k8s.BootstrapNamespace(c.Request.Context(), a.K8sManager.Clientset, namespace); err != nil {
				log.Info("Lazy bootstrap failed for namespace", "namespace", namespace, "err", err)
			} else {
				a.bootstrappedUsers.Store(namespace, true)
			}
		}

		c.Set(UserKey, user)
		c.Set(NamespaceKey, namespace)
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

func (a *Authenticator) GetNamespaceFromContext(c *gin.Context) string {
	val, exists := c.Get(NamespaceKey)
	if !exists {
		// Fallback to UserKey if NamespaceKey is missing (should not happen due to Middleware)
		return a.GetUserFromContext(c)
	}
	if ns, ok := val.(string); ok {
		return ns
	}
	return ""
}
