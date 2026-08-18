package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
	"k8s.io/klog/v2"
)

var overseerQueueMu sync.Mutex

func startQueueHTTPServer(ctx context.Context, queueDir string, addr string) {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v1/queue", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		overseerQueueMu.Lock()
		resp := buildQueueResponse(queueDir)
		overseerQueueMu.Unlock()
		_ = json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/api/v1/queue/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/queue/"), "/")
		if len(parts) == 0 || parts[0] == "" {
			http.Error(w, "Task filename required", http.StatusBadRequest)
			return
		}
		filename := filepath.Base(parts[0])

		if r.Method == http.MethodDelete {
			incomingPath := filepath.Join(queueDir, "incoming", filename)
			overseerQueueMu.Lock()
			err := os.Remove(incomingPath)
			overseerQueueMu.Unlock()
			if err != nil && !os.IsNotExist(err) {
				http.Error(w, fmt.Sprintf("Failed to remove task: %v", err), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleted", "fileName": filename})
			return
		}

		if r.Method == http.MethodPost && len(parts) >= 2 && parts[1] == "priority" {
			var body struct {
				Priority string `json:"priority"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Priority == "" {
				http.Error(w, "Invalid JSON body, priority required", http.StatusBadRequest)
				return
			}

			incomingPath := filepath.Join(queueDir, "incoming", filename)
			overseerQueueMu.Lock()
			content, err := os.ReadFile(incomingPath)
			if err != nil {
				overseerQueueMu.Unlock()
				http.Error(w, fmt.Sprintf("Failed to read task file: %v", err), http.StatusNotFound)
				return
			}

			re := regexp.MustCompile(`(?m)^priority:.*$`)
			newContent := re.ReplaceAllString(string(content), fmt.Sprintf("priority: %s", body.Priority))
			if !re.MatchString(string(content)) {
				newContent += fmt.Sprintf("\npriority: %s\n", body.Priority)
			}

			err = os.WriteFile(incomingPath, []byte(newContent), 0644)
			overseerQueueMu.Unlock()
			if err != nil {
				http.Error(w, fmt.Sprintf("Failed to write task file: %v", err), http.StatusInternalServerError)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "updated", "priority": body.Priority, "fileName": filename})
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

func buildQueueResponse(queueDir string) QueueResponse {
	readQueueDir := func(sub string) []taskItem {
		d := filepath.Join(queueDir, sub)
		entries, err := os.ReadDir(d)
		if err != nil {
			return nil
		}
		var items []taskItem
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(d, e.Name()))
			if err != nil {
				continue
			}
			var t QueueTask
			if err := yaml.Unmarshal(data, &t); err != nil {
				continue
			}
			if t.Priority == "" {
				t.Priority = "medium"
			}
			if t.EnqueuedAt.IsZero() {
				var modTime time.Time
				if info, err := e.Info(); err == nil {
					modTime = info.ModTime()
				}
				t.EnqueuedAt = getEnqueueTime(&t, modTime)
			}
			items = append(items, taskItem{
				filename: e.Name(),
				task:     &t,
			})
		}
		return items
	}

	taskToItem := func(item taskItem, sub string) QueueTaskItem {
		t := item.task
		tPrio := strings.ToLower(t.Priority)
		if tPrio == "" {
			tPrio = "medium"
		}
		var createdStr, enqueuedStr string
		if !t.CreatedAt.IsZero() {
			createdStr = t.CreatedAt.Format(time.RFC3339)
		}
		if !t.EnqueuedAt.IsZero() {
			enqueuedStr = t.EnqueuedAt.Format(time.RFC3339)
		}
		return QueueTaskItem{
			FileName:   item.filename,
			QueueState: sub,
			Type:       t.Type,
			URL:        t.URL,
			Number:     t.Number,
			Priority:   tPrio,
			Phase:      t.Phase,
			CreatedAt:  createdStr,
			EnqueuedAt: enqueuedStr,
			Assignee:   t.Assignee,
			Status:     t.Status,
			CommitSHA:  t.CommitSHA,
		}
	}

	incomingItems := readQueueDir("incoming")
	sortedIncoming := sortTasksFairly(incomingItems)

	var incoming []QueueTaskItem
	for i, item := range sortedIncoming {
		m := taskToItem(item, "incoming")
		m.Rank = i + 1
		incoming = append(incoming, m)
	}

	processingItems := readQueueDir("processing")
	var processing []QueueTaskItem
	for _, item := range processingItems {
		processing = append(processing, taskToItem(item, "processing"))
	}

	processedItems := readQueueDir("processed")
	var processed []QueueTaskItem
	for _, item := range processedItems {
		processed = append(processed, taskToItem(item, "processed"))
	}

	byPrio := make(map[string]int)
	byType := make(map[string]int)
	for _, item := range incoming {
		byPrio[item.Priority]++
		byType[item.Type]++
	}

	if len(processed) > 20 {
		processed = processed[:20]
	}

	return QueueResponse{
		Summary: QueueSummary{
			TotalPending:    len(incoming),
			TotalProcessing: len(processing),
			TotalCompleted:  len(processed),
			ByPriority:      byPrio,
			ByType:          byType,
		},
		Incoming:   incoming,
		Processing: processing,
		Processed:  processed,
	}
}
