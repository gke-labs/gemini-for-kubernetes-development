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

// Package agentserver provides a simple HTTP server that runs within the agent's environment.
// It is primarily used to expose logs and health status to the Gemini for Kubernetes UI.
package agentserver

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"k8s.io/klog/v2"
)

const (
	// ServerPort is the port where the agent server listens.
	ServerPort = 13339
	// LogsDirectory is the directory where the agent writes its logs.
	LogsDirectory = "/workspaces/.agent/logs"
)

// AgentServer encapsulates the HTTP server for the agent.
type AgentServer struct {
	server *http.Server
}

// NewAgentServer creates a new instance of AgentServer with configured routes.
func NewAgentServer() *AgentServer {
	r := mux.NewRouter()
	// Health check endpoint for readiness probes.
	r.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	// Endpoint to serve specific log files by task ID.
	r.HandleFunc("/logs/{taskID}", serveLogFile)

	// Add CORS middleware to allow requests from the UI (potentially running on a different origin).
	corsObj := handlers.AllowedOrigins([]string{"*"})

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", ServerPort),
		Handler: handlers.CORS(corsObj)(r),
	}

	return &AgentServer{
		server: srv,
	}
}

// Start initializes the log directory and starts the HTTP server in a background goroutine.
func (s *AgentServer) Start() error {
	// Ensure logs directory exists
	if err := os.MkdirAll(LogsDirectory, 0755); err != nil {
		return fmt.Errorf("failed to create logs directory: %w", err)
	}

	klog.Infof("AgentServer listening on %d", ServerPort)
	go func() {
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			klog.Errorf("AgentServer failed: %v", err)
		}
	}()
	return nil
}

// Stop gracefully shuts down the server.
func (s *AgentServer) Stop() error {
	return s.server.Close()
}

// serveLogFile handles requests to read log files.
// It validates the taskID to prevent path traversal and serves the file if it exists.
func serveLogFile(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	taskID := vars["taskID"]

	// Sanitize taskID to prevent path traversal
	if taskID == "" || filepath.Clean(taskID) != taskID || taskID == ".." || taskID == "." {
		http.Error(w, "Invalid task ID", http.StatusBadRequest)
		return
	}

	logFilePath := filepath.Join(LogsDirectory, taskID+".log")

	// Check if file exists
	if _, err := os.Stat(logFilePath); os.IsNotExist(err) {
		http.Error(w, "Log file not found", http.StatusNotFound)
		return
	}

	// Serve the file
	// http.ServeFile handles Range requests and caching headers automatically,
	// but for live streaming (tailing), we might want a WebSocket or chunked response.
	// For now, simple file serving is a good start. The UI can poll or use Range headers.
	http.ServeFile(w, r, logFilePath)
}
