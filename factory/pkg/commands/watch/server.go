package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/concurrency"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/geminitokens"
)

func startQueueHTTPServer(ctx context.Context, queueMgr *concurrency.TaskQueueManager, addr string) {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v1/queue", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		resp := queueMgr.GetQueueResponse()
		_ = json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/api/v1/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		status, err := geminitokens.GetTokensStatus()
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to get tokens status: %v", err), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(status)
	})

	mux.HandleFunc("/api/v1/queue/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/queue/"), "/")
		if len(parts) == 0 || parts[0] == "" {
			http.Error(w, "Task filename required", http.StatusBadRequest)
			return
		}
		filename := filepath.Base(parts[0])

		if r.Method == http.MethodDelete {
			if err := queueMgr.RemoveTask(filename); err != nil && !os.IsNotExist(err) {
				http.Error(w, fmt.Sprintf("Failed to remove task: %v", err), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleted", "fileName": filename})
			return
		}

		if r.Method == http.MethodPost && len(parts) >= 2 && parts[1] == "priority" {
			var body struct {
				Priority api.TaskPriority `json:"priority"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Priority == "" {
				http.Error(w, "Invalid JSON body, priority required", http.StatusBadRequest)
				return
			}

			if err := queueMgr.UpdateTaskPriority(filename, body.Priority); err != nil {
				http.Error(w, fmt.Sprintf("Failed to update priority: %v", err), http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "updated", "priority": string(body.Priority), "fileName": filename})
			return
		}

		http.Error(w, "Not found", http.StatusNotFound)
	})

	server := &http.Server{Addr: addr, Handler: mux}
	klog.Infof("Starting embedded Overseer Queue HTTP server on %s", addr)
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			klog.Warningf("Overseer Queue HTTP server error: %v", err)
		}
	}()

	<-ctx.Done()
	_ = server.Close()
}
