package api

import (
	"net/http"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// overseerAllows reports whether the signed-in user may see and drive ov,
// all of it: an admin, a login in its spec.allowlistedUsers, or a
// maintainer (push access) of the repository it watches, as the member's
// own GitHub token reports it. The admin list stays for what is not one
// overseer's (switching namespaces, other members' sandboxes).
func (s *Server) overseerAllows(c *gin.Context, ov *unstructured.Unstructured) bool {
	user := s.Auth.GetUserFromContext(c)
	if user == "" {
		return false
	}
	if s.Auth.IsUserAdmin(user) {
		return true
	}
	allowlisted, _, _ := unstructured.NestedStringSlice(ov.Object, "spec", "allowlistedUsers")
	if slices.ContainsFunc(allowlisted, func(login string) bool { return strings.EqualFold(login, user) }) {
		return true
	}
	repoURL, _, _ := unstructured.NestedString(ov.Object, "spec", "repoURL")
	return repoURL != "" && s.hasPushPermission(c.Request.Context(), s.Auth.GetNamespaceFromContext(c), user, repoURL)
}

// overseerAccess guards /api/overseers/:name/…: the overseer must exist and
// overseerAllows the user. Not found and not allowed answer alike, so the
// routes do not say which overseers exist to those who may see none.
func (s *Server) overseerAccess() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !safeTaskName.MatchString(c.Param("name")) {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid overseer name"})
			return
		}
		ov, err := s.K8sManager.GetOverseer(c.Request.Context(), c.Param("name"))
		if err != nil && !apierrors.IsNotFound(err) {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "failed to get overseer"})
			return
		}
		if err != nil || !s.overseerAllows(c, ov) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "not an admin, a maintainer of this overseer's repository, or on its allowlist"})
			return
		}
		c.Next()
	}
}
