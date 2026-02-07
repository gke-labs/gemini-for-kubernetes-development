package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	repoagent "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent"
)

func (s *Server) getVersion(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"version": repoagent.REPOAGENT_RELEASE_VERSION,
		"commit":  repoagent.GitCommit,
	})
}