// Copyright 2026 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package api

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/gin-gonic/gin"
	"k8s.io/klog/v2"
)

func (s *Server) proxySandbox(c *gin.Context) {
	namespace := c.Param("namespace")
	name := c.Param("name")
	// The path parameter will include the leading slash, e.g. "/some/file"
	proxyPath := c.Param("path")

	user := s.Auth.GetUserFromContext(c)
	if user == "" {
		c.String(http.StatusUnauthorized, "Unauthorized")
		return
	}
	if user != namespace && !s.Auth.IsUserAdmin(user) {
		c.String(http.StatusForbidden, "Forbidden")
		klog.Infof("Unauthorized access %s for %s", user, c.Request.URL.String())
		return
	}

	// The sandbox service name pattern from RGD: devc-<name>-lb
	targetHost := fmt.Sprintf("devc-%s-lb.%s.svc.cluster.local:13338", name, namespace)

	targetURL := &url.URL{
		Scheme: "http",
		Host:   targetHost,
	}

	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	//This forces the proxy to flush data to the client immediately after writing to the response body, instead of
	// buffering. This is crucial for interactive applications and streaming protocols, ensuring low latency.
	proxy.FlushInterval = -1

	// Customize the director to rewrite the path and set headers
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)

		// Set the Host header to the target service
		req.Host = targetHost

		// Rewrite the path.
		// The request coming in is /sandbox/:namespace/:name/*path
		// The target expects /*path (from root)
		req.URL.Path = proxyPath

		// If the path was empty or just "/", proxyPath might be "/" or empty depending on Gin.
		// Ensure we don't send empty path if root is requested.
		if req.URL.Path == "" {
			req.URL.Path = "/"
		}

		// Strip Origin header to prevent VS Code Server from rejecting the request due to CORS/Origin mismatch
		// VS Code Server (and many other web applications) strictly validates the Origin header against the Host header to
		// prevent CSRF. Since the proxy changes the Host header to the internal service name but the browser sends the external
		// Origin, this mismatch often causes the backend to reject the connection (especially WebSocket upgrades) immediately,
		// leading to a 1006 Close error. Removing it forces the backend to treat it as a same-origin request or skip the check.
		req.Header.Del("Origin")

		// Set X-Forwarded headers
		// Explicitly setting X-Forwarded-Host and X-Forwarded-Proto allows the backend to know the original external URL, which
		// is often needed for generating correct self-referencing links or redirects
		req.Header.Set("X-Forwarded-Host", c.Request.Host)
		if c.Request.TLS != nil {
			req.Header.Set("X-Forwarded-Proto", "https")
		} else {
			req.Header.Set("X-Forwarded-Proto", "http")
		}
	}

	// Error handling
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		// Log the error
		klog.Infof("Proxy error for URL %s: %v\n", r.URL.String(), err)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(fmt.Sprintf("Bad Gateway: %v", err)))
	}

	proxy.ServeHTTP(c.Writer, c.Request)
}
