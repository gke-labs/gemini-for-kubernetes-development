// Package agentserver provides a simple HTTP server that runs within the agent's environment.
// It is primarily used to expose logs and health status to the Gemini for Kubernetes UI.
package agentserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"k8s.io/klog/v2"
)

const (
	// ServerPort is the port where the agent server listens.
	ServerPort = 13339
)

var (
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

	// Endpoint to list tasks directly from /workspaces/tasks
	r.HandleFunc("/tasks", serveTasksList)

	// Endpoint to serve specific log files by task ID.
	r.HandleFunc("/logs/{taskID}", serveLogFile)

	// Endpoint to serve tool telemetry by task ID.
	r.HandleFunc("/telemetry/{taskID}", serveTelemetryFile)

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

func serveTasksList(w http.ResponseWriter, r *http.Request) {
	tasksDir := "/workspaces/tasks"
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
		return
	}

	var res []map[string]interface{}
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if !entry.IsDir() {
			continue
		}
		d := entry.Name()
		p := filepath.Join(tasksDir, d)
		status := "Pending"
		var ec interface{}

		if exitBytes, err := os.ReadFile(filepath.Join(p, "exit_code")); err == nil && len(strings.TrimSpace(string(exitBytes))) > 0 {
			code := strings.TrimSpace(string(exitBytes))
			ec = code
			if code == "0" {
				status = "Completed"
			} else {
				status = "Failed"
			}
		} else if pidBytes, err := os.ReadFile(filepath.Join(p, "pid")); err == nil && len(strings.TrimSpace(string(pidBytes))) > 0 {
			pidStr := strings.TrimSpace(string(pidBytes))
			var pid int
			if _, err := fmt.Sscanf(pidStr, "%d", &pid); err == nil {
				startBytes, _ := os.ReadFile(filepath.Join(p, "start_time"))
				if isProcessAliveAndNotZombie(pid, strings.TrimSpace(string(startBytes))) {
					status = "Running"
				} else {
					status = "Crashed"
					ec = "137"
				}
			}
		}

		res = append(res, map[string]interface{}{
			"metadata": map[string]interface{}{
				"name": d,
			},
			"spec": map[string]interface{}{
				"taskType": d,
			},
			"status": map[string]interface{}{
				"state":    status,
				"exitCode": ec,
			},
		})
	}
	if res == nil {
		res = []map[string]interface{}{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
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
		// Also check /workspaces/tasks/<taskID>/execution.log and agent-output.txt
		taskLogPath := filepath.Join("/workspaces/tasks", taskID, "execution.log")
		if _, err2 := os.Stat(taskLogPath); os.IsNotExist(err2) {
			outputPath := filepath.Join("/workspaces/tasks", taskID, "agent-output.txt")
			if _, err3 := os.Stat(outputPath); os.IsNotExist(err3) {
				http.Error(w, "Log file not found", http.StatusNotFound)
				return
			}
			logFilePath = outputPath
		} else {
			logFilePath = taskLogPath
		}
	}

	// Serve the file
	http.ServeFile(w, r, logFilePath)
}

// serveTelemetryFile handles requests to read tool telemetry files.
func serveTelemetryFile(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	taskID := vars["taskID"]

	if taskID == "" || filepath.Clean(taskID) != taskID || taskID == ".." || taskID == "." {
		http.Error(w, "Invalid task ID", http.StatusBadRequest)
		return
	}

	telemetryPath := filepath.Join("/workspaces/tasks", taskID, "tool-telemetry.json")
	if _, err := os.Stat(telemetryPath); os.IsNotExist(err) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	http.ServeFile(w, r, telemetryPath)
}

func isProcessAliveAndNotZombie(pid int, expectedStartTime string) bool {
	proc, err := os.FindProcess(pid)
	if err != nil || proc.Signal(syscall.Signal(0)) != nil {
		return false
	}
	statBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err == nil {
		parts := strings.Fields(string(statBytes))
		if len(parts) >= 3 && strings.HasPrefix(parts[2], "Z") {
			return false
		}
	}
	if expectedStartTime != "" {
		out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "lstart=").Output()
		if err != nil {
			return false
		}
		currentStart := strings.Join(strings.Fields(string(out)), " ")
		expectedStart := strings.Join(strings.Fields(expectedStartTime), " ")
		if currentStart != expectedStart {
			return false
		}
	}
	return true
}
