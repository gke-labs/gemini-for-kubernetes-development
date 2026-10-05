package taskapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/acpd"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/envd"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/spool"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskoutput"
	"k8s.io/klog/v2"
)

// maxPostBytes bounds a posted task: a recipe and its inputs, a big PR's
// prompt included, are well under it.
const maxPostBytes = 16 << 20

// name is what a task id or a file name in a task directory may be: one
// path element, never a dot-name, so no request reaches outside it.
var name = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)

// Server serves the tasks in one sandbox. It runs in the sandbox's daemon,
// the parent of every task it starts.
type Server struct {
	incomingDir, tasksDir string
	launch                spool.Launcher
	// poll is how often a followed log is read for more.
	poll time.Duration
	// deadGrace is how long a task's process may be gone without an exit
	// code before it is given 137: the script writes the code just after.
	deadGrace time.Duration

	// sessions, when set, serves agent conversations under /v1/sessions,
	// and tokens holds the tokens the tasks drive theirs with.
	sessions *acpd.Server
	tokens   *Tokens

	// mu makes finding a task by id or run name and starting it one step,
	// so the same task posted twice starts once.
	mu sync.Mutex
}

// NewServer serves the tasks in tasksDir, and the spooled ones still in
// incomingDir, starting posted ones with launch.
func NewServer(incomingDir, tasksDir string, launch spool.Launcher) *Server {
	return &Server{incomingDir: incomingDir, tasksDir: tasksDir, launch: launch, poll: 500 * time.Millisecond, deadGrace: 5 * time.Second}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/version", func(w http.ResponseWriter, r *http.Request) {
		v := Version{API: APIVersion}
		if s.sessions != nil {
			v.Sessions = SessionsVersion
		}
		writeJSON(w, http.StatusOK, v)
	})
	mux.HandleFunc("GET /v1/tasks", s.handleList)
	mux.HandleFunc("POST /v1/tasks", s.handlePost)
	mux.HandleFunc("GET /v1/tasks/{id}", s.handleGet)
	mux.HandleFunc("GET /v1/tasks/{id}/log", s.handleLog)
	mux.HandleFunc("GET /v1/tasks/{id}/files", s.handleFiles)
	mux.HandleFunc("GET /v1/tasks/{id}/files/{name}", s.handleReadFile)
	mux.HandleFunc("PUT /v1/tasks/{id}/files/{name}", s.handleWriteFile)
	mux.HandleFunc("POST /v1/tasks/{id}/cancel", s.handleCancel)
	if s.sessions != nil {
		mux.Handle("/v1/sessions", s.sessionsHandler())
		mux.Handle("/v1/sessions/", s.sessionsHandler())
	}
	return mux
}

// Serve serves s on addr until ctx ends.
func Serve(ctx context.Context, addr string, s *Server) error {
	log := klog.FromContext(ctx)
	httpServer := &http.Server{
		Addr:    addr,
		Handler: s.Handler(),
		// No write timeout: a followed log is as long as its task.
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	log.Info("task server listening", "addr", addr)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("task server: %w", err)
	}
	return nil
}

// list is every task, as spool.List finds them through envd.
func (s *Server) list(ctx context.Context) ([]spool.Entry, error) {
	out, err := exec.CommandContext(ctx, "sh", "-c", spool.ListScript(s.incomingDir, s.tasksDir)).Output()
	if err != nil {
		return nil, fmt.Errorf("listing tasks: %w", err)
	}
	entries := spool.ParseList(string(out))
	for i := range entries {
		if entries[i].State != spool.Pending {
			spool.Overlay(&entries[i], filepath.Join(s.tasksDir, entries[i].ID))
		}
	}
	return entries, nil
}

func (s *Server) find(ctx context.Context, id string) (spool.Entry, bool, error) {
	entries, err := s.list(ctx)
	if err != nil {
		return spool.Entry{}, false, err
	}
	for _, e := range entries {
		if e.ID == id {
			return e, true, nil
		}
	}
	return spool.Entry{}, false, nil
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	entries, err := s.list(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if entries == nil {
		entries = []spool.Entry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	id, ok := pathName(w, r, "id")
	if !ok {
		return
	}
	e, found, err := s.find(r.Context(), id)
	switch {
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	case !found:
		writeError(w, http.StatusNotFound, fmt.Sprintf("no task %s", id))
	default:
		writeJSON(w, http.StatusOK, e)
	}
}

// handlePost starts a recipe task in a task directory of its own and
// answers once its process has started, so the client can follow it at
// once. A task posted again — the client lost the answer, or a retry
// reuses a run name — is answered with the one there.
func (s *Server) handlePost(w http.ResponseWriter, r *http.Request) {
	var req PostRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPostBytes)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("decoding the task: %v", err))
		return
	}
	task := req.Task
	if !name.MatchString(task.ID) || task.Recipe == "" || len(req.Recipe) == 0 {
		writeError(w, http.StatusBadRequest, "a task needs an id, a recipe name and a recipe")
		return
	}
	ctx := r.Context()
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.list(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, e := range entries {
		if e.ID == task.ID || (task.RunName != "" && e.RunName == task.RunName) {
			writeJSON(w, http.StatusOK, PostResponse{Task: e, Existed: true})
			return
		}
	}

	taskDir := filepath.Join(s.tasksDir, task.ID)
	if err := writeTask(taskDir, req); err != nil {
		_ = os.RemoveAll(taskDir)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.launch(taskDir, req.Env); err != nil {
		spool.Fail(taskDir, err)
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("starting task %s: %v", task.ID, err))
		return
	}
	klog.FromContext(ctx).Info("task server: started task", "task", task.ID, "recipe", task.Recipe, "runName", task.RunName)
	s.awaitStarted(ctx, taskDir)
	e, found, err := s.find(ctx, task.ID)
	if err != nil || !found {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("task %s started but cannot be found: %v", task.ID, err))
		return
	}
	writeJSON(w, http.StatusCreated, PostResponse{Task: e})
}

// writeTask writes what the task's process reads, as a claimed spooled
// task has it, minus its environment.
func writeTask(taskDir string, req PostRequest) error {
	if err := os.MkdirAll(filepath.Dir(taskDir), 0o755); err != nil {
		return err
	}
	if err := os.Mkdir(taskDir, 0o700); err != nil {
		return fmt.Errorf("creating task directory: %w", err)
	}
	taskJSON, err := json.Marshal(req.Task)
	if err != nil {
		return err
	}
	inputsJSON, err := json.Marshal(req.Inputs)
	if err != nil {
		return err
	}
	for _, f := range []struct {
		name string
		data []byte
	}{{spool.RecipeFile, req.Recipe}, {spool.InputsFile, inputsJSON}, {spool.TaskFile, taskJSON}} {
		if err := os.WriteFile(filepath.Join(taskDir, f.name), f.data, 0o600); err != nil {
			return fmt.Errorf("writing %s: %w", f.name, err)
		}
	}
	return nil
}

// awaitStarted waits for the task script to record its pid, or its exit,
// which it does first thing.
func (s *Server) awaitStarted(ctx context.Context, taskDir string) {
	tf := envd.NewTaskFiles(taskDir)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && !nonEmpty(tf.PIDFile) && !nonEmpty(tf.ExitCodeFile) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// handleLog writes the task's log from offset, and with follow (the
// default) goes on writing it until the task has ended. A task whose
// process is gone without an exit code is given 137, as attaching
// through envd gives it.
func (s *Server) handleLog(w http.ResponseWriter, r *http.Request) {
	taskDir, ok := s.taskDir(w, r)
	if !ok {
		return
	}
	offset := int64(0)
	if raw := r.URL.Query().Get("offset"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid offset %q", raw))
			return
		}
		offset = parsed
	}
	follow := r.URL.Query().Get("follow") != "false"
	tf := envd.NewTaskFiles(taskDir)

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	flush := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}
	flush()

	ctx := r.Context()
	var goneSince time.Time
	for {
		n, err := copyFrom(w, tf.LogFile, offset)
		offset += n
		if err != nil {
			return
		}
		if n > 0 {
			flush()
		}
		if !follow {
			return
		}
		st, hasStatus := spool.ReadStatus(taskDir)
		if (hasStatus && st.State == spool.Exited) || (!hasStatus && nonEmpty(tf.ExitCodeFile)) {
			// What the task wrote between the last read and its end.
			n, _ := copyFrom(w, tf.LogFile, offset)
			if n > 0 {
				flush()
			}
			return
		}
		// The daemon records the end of a task it started; only one it
		// did not, started through envd, is judged by its pid.
		if !hasStatus && nonEmpty(tf.PIDFile) && !alive(tf) {
			if goneSince.IsZero() {
				goneSince = time.Now()
			} else if time.Since(goneSince) > s.deadGrace {
				writeIfMissing(tf.ExitCodeFile, 137)
				continue
			}
		} else {
			goneSince = time.Time{}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.poll):
		}
	}
}

// copyFrom writes path's bytes from offset on to w.
func copyFrom(w io.Writer, path string, offset int64) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		// No log yet.
		return 0, nil
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return 0, err
	}
	return io.Copy(w, f)
}

func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request) {
	taskDir, ok := s.taskDir(w, r)
	if !ok {
		return
	}
	entries, err := os.ReadDir(taskDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	names := []string{}
	for _, e := range entries {
		if !e.IsDir() && e.Name() != spool.EnvFile && !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	writeJSON(w, http.StatusOK, names)
}

func (s *Server) handleReadFile(w http.ResponseWriter, r *http.Request) {
	taskDir, ok := s.taskDir(w, r)
	if !ok {
		return
	}
	file, ok := pathName(w, r, "name")
	if !ok {
		return
	}
	if file == spool.EnvFile {
		writeError(w, http.StatusForbidden, "a task's environment is not served")
		return
	}
	data, err := os.ReadFile(filepath.Join(taskDir, file))
	switch {
	case os.IsNotExist(err):
		writeError(w, http.StatusNotFound, fmt.Sprintf("no file %s", file))
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	default:
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(data)
	}
}

// writableFiles are the files a client may write in a task directory: the
// mark that the task's result was applied, which the next run reads.
var writableFiles = map[string]bool{taskoutput.AppliedFile: true}

func (s *Server) handleWriteFile(w http.ResponseWriter, r *http.Request) {
	taskDir, ok := s.taskDir(w, r)
	if !ok {
		return
	}
	file, ok := pathName(w, r, "name")
	if !ok {
		return
	}
	if !writableFiles[file] {
		writeError(w, http.StatusForbidden, fmt.Sprintf("%s is not a file clients write", file))
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.WriteFile(filepath.Join(taskDir, file), data, 0o644); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleCancel ends the task's process group, not only its first process:
// the recipe runner, the agent it started and whatever that started. A
// kill records 137 even over an exit code the task wrote, as a quota kill
// through envd does; a cancel keeps the task's own.
func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	taskDir, ok := s.taskDir(w, r)
	if !ok {
		return
	}
	var req CancelRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("decoding the cancel: %v", err))
			return
		}
	}
	tf := envd.NewTaskFiles(taskDir)
	sig, code := syscall.SIGTERM, 143
	if req.Kill {
		sig, code = syscall.SIGKILL, 137
	}
	if !nonEmpty(tf.ExitCodeFile) && alive(tf) {
		if pid, err := readPID(tf); err == nil {
			signalGroup(pid, sig)
		}
	}
	if req.Kill {
		_ = os.WriteFile(tf.ExitCodeFile, []byte(fmt.Sprintf("%d\n", code)), 0o644)
		spool.RecordKill(taskDir)
	} else {
		writeIfMissing(tf.ExitCodeFile, code)
	}
	id := filepath.Base(taskDir)
	// The task's agent goes with it.
	if s.sessions != nil {
		s.sessions.CloseTask(id)
	}
	klog.FromContext(r.Context()).Info("task server: cancelled task", "task", id, "kill", req.Kill)
	e, found, err := s.find(r.Context(), id)
	if err != nil || !found {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("task %s cancelled but cannot be found: %v", id, err))
		return
	}
	writeJSON(w, http.StatusOK, e)
}

// signalGroup signals pid's process group, or pid alone when it shares
// this process's group: a task started through envd, not by the daemon,
// may, and the daemon must not signal itself.
func signalGroup(pid int, sig syscall.Signal) {
	if pgid, err := syscall.Getpgid(pid); err == nil && pgid != syscall.Getpgrp() {
		_ = syscall.Kill(-pgid, sig)
		return
	}
	_ = syscall.Kill(pid, sig)
}

// taskDir is the directory of the task the request names, answering 404
// when there is none.
func (s *Server) taskDir(w http.ResponseWriter, r *http.Request) (string, bool) {
	id, ok := pathName(w, r, "id")
	if !ok {
		return "", false
	}
	dir := filepath.Join(s.tasksDir, id)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		writeError(w, http.StatusNotFound, fmt.Sprintf("no task %s", id))
		return "", false
	}
	return dir, true
}

func pathName(w http.ResponseWriter, r *http.Request, key string) (string, bool) {
	v := r.PathValue(key)
	if !name.MatchString(v) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid %s %q", key, v))
		return "", false
	}
	return v, true
}

// alive is whether the task's process still runs. Its pid cannot have
// been reused by another: the daemon marks every task of an earlier
// container ended before it starts any.
func alive(tf envd.TaskFiles) bool {
	pid, err := readPID(tf)
	if err != nil {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil && !errors.Is(err, syscall.EPERM) {
		return false
	}
	// A zombie is gone, only not reaped yet.
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return true
	}
	if i := strings.LastIndexByte(string(stat), ')'); i >= 0 && i+2 < len(stat) {
		return stat[i+2] != 'Z'
	}
	return true
}

func readPID(tf envd.TaskFiles) (int, error) {
	data, err := os.ReadFile(tf.PIDFile)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}

func writeIfMissing(path string, code int) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "%d\n", code)
}

func nonEmpty(path string) bool {
	data, err := os.ReadFile(path)
	return err == nil && strings.TrimSpace(string(data)) != ""
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg})
}
