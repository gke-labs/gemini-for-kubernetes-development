package api

import (
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
)

// usageCollectorURL returns the base URL of the token-usage collector
// service (factory token-daemon, see factory/pkg/tokenusage).
func usageCollectorURL() string {
	if v, ok := os.LookupEnv("COLLECTOR_URL"); ok {
		return strings.TrimRight(v, "/")
	}
	return "http://token-usage.overseer-system.svc.cluster.local:8080"
}

// proxyUsage forwards GET /api/usage/*path to the token-usage collector,
// e.g. /api/usage/v1/usage/rollups/workflows -> <collector>/v1/usage/rollups/workflows.
func (s *Server) proxyUsage(c *gin.Context) {
	base := usageCollectorURL()
	if base == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "token-usage collector is not configured (COLLECTOR_URL)"})
		return
	}

	target := base + "/" + strings.TrimPrefix(c.Param("path"), "/")
	if q := c.Request.URL.RawQuery; q != "" {
		target += "?" + q
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, target, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to build collector request", "details": err.Error()})
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to reach token-usage collector", "details": err.Error()})
		return
	}
	defer resp.Body.Close()

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	c.DataFromReader(resp.StatusCode, resp.ContentLength, contentType, resp.Body, nil)
}
