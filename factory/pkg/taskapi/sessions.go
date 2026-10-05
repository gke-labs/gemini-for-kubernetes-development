package taskapi

import (
	"crypto/rand"
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
}

func NewTokens() *Tokens {
	return &Tokens{m: map[string]string{}}
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
		t.mu.Lock()
		t.m[filepath.Base(taskDir)] = token
		t.mu.Unlock()
		return launch(taskDir, withToken)
	}
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

// Running is whether the task has not ended: started or about to be.
func (t taskSessions) Running(task string) bool {
	dir, err := t.Dir(task)
	if err != nil {
		return false
	}
	if st, ok := spool.ReadStatus(dir); ok {
		return st.State != spool.Exited
	}
	return !nonEmpty(envd.NewTaskFiles(dir).ExitCodeFile)
}

func (t taskSessions) Token(task string) string {
	return t.s.tokens.get(task)
}

// sessionsHandler is acpd's routes under /v1.
func (s *Server) sessionsHandler() http.Handler {
	return http.StripPrefix("/v1", s.sessions.Handler())
}
