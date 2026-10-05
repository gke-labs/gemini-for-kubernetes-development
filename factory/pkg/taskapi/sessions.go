package taskapi

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/acpd"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/envd"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/spool"
)

// EnvTaskToken carries a task's token into its process: what the task
// sends, in acpd.TaskTokenHeader, to start and drive its own session while
// everybody else may only watch it.
const EnvTaskToken = "FACTORY_TASK_TOKEN"

// SessionsVersion is the agent-session API this server serves under
// /v1/sessions, reported beside the task API's version.
const SessionsVersion = 1

// Tokens mints each task's token as it is launched and keeps it in memory
// only: a task outliving the daemon does not happen, the container goes
// with it.
type Tokens struct {
	mu sync.Mutex
	m  map[string]string
	// revises are, by task, the session each revise launched continues.
	revises map[string]string
}

func NewTokens() *Tokens {
	return &Tokens{m: map[string]string{}, revises: map[string]string{}}
}

// Wrap is launch, with the task's token added to its environment.
func (t *Tokens) Wrap(launch spool.Launcher) spool.Launcher {
	return func(taskDir string, env map[string]string) error {
		token := rand.Text()
		withToken := make(map[string]string, len(env)+1)
		for k, v := range env {
			withToken[k] = v
		}
		withToken[EnvTaskToken] = token
		id := filepath.Base(taskDir)
		t.mu.Lock()
		t.m[id] = token
		if session := taskSession(taskDir); session != "" {
			t.revises[id] = session
		}
		t.mu.Unlock()
		return launch(taskDir, withToken)
	}
}

// taskSession is the session task.json says the task continues, "" for
// a task that starts its own.
func taskSession(taskDir string) string {
	data, err := os.ReadFile(filepath.Join(taskDir, spool.TaskFile))
	if err != nil {
		return ""
	}
	var task spool.Task
	if json.Unmarshal(data, &task) != nil {
		return ""
	}
	return task.Session
}

// revisesOf are the revises launched into session's conversation.
func (t *Tokens) revisesOf(session string) []string {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []string
	for task, s := range t.revises {
		if s == session {
			out = append(out, task)
		}
	}
	return out
}

func (t *Tokens) get(task string) string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.m[task]
}

// HostSessions serves sessions's agent conversations under /v1/sessions,
// with sessions that belong to this server's tasks: kept in the task's
// directory, the task's to drive while it runs, closed when it is
// cancelled.
func (s *Server) HostSessions(sessions *acpd.Server, tokens *Tokens) {
	s.sessions, s.tokens = sessions, tokens
	sessions.SetTasks(taskSessions{s})
}

// taskSessions is what acpd asks about the server's tasks.
type taskSessions struct{ s *Server }

func (t taskSessions) Dir(task string) (string, error) {
	if !name.MatchString(task) {
		return "", fmt.Errorf("invalid task %q", task)
	}
	dir := filepath.Join(t.s.tasksDir, task)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return "", fmt.Errorf("no task %s", task)
	}
	return dir, nil
}

// Running is whether the task's session is a task's to drive: the task
// has not ended, or a revise of it runs.
func (t taskSessions) Running(task string) bool {
	return t.running(task) || t.revising(task) != ""
}

// revising is the revise running in task's session, if any.
func (t taskSessions) revising(task string) string {
	for _, r := range t.s.tokens.revisesOf(task) {
		if t.running(r) {
			return r
		}
	}
	return ""
}

// running is whether the task has not ended: started or about to be.
func (t taskSessions) running(task string) bool {
	dir, err := t.Dir(task)
	if err != nil {
		return false
	}
	if st, ok := spool.ReadStatus(dir); ok {
		return st.State != spool.Exited
	}
	return !nonEmpty(envd.NewTaskFiles(dir).ExitCodeFile)
}

// Token is the token of whoever drives task's session: a revise running
// in it, else the task.
func (t taskSessions) Token(task string) string {
	if r := t.revising(task); r != "" {
		return t.s.tokens.get(r)
	}
	return t.s.tokens.get(task)
}

// sessionsHandler is acpd's routes under /v1.
func (s *Server) sessionsHandler() http.Handler {
	return http.StripPrefix("/v1", s.sessions.Handler())
}
