package taskapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/envd"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/spool"
	"k8s.io/klog/v2"
)

// EnvTransport set to "envd" makes Connect use envd even where a sandbox
// has a task server: the way back, should the server misbehave.
const EnvTransport = "FACTORY_TASK_TRANSPORT"

// Sandbox is the tasks in one sandbox, reached through its task server or
// through envd.
type Sandbox interface {
	Name() string
	// Transport is "task server" or "envd", for messages.
	Transport() string
	// List is every task, newest first.
	List(ctx context.Context) ([]spool.Entry, error)
	// Start hands the sandbox a recipe task and returns once it has
	// started. spool.ErrNotClaimed: the sandbox has no daemon to start it,
	// and nothing of it runs.
	Start(ctx context.Context, task spool.Task, recipe []byte, inputs, env map[string]string) error
	// AwaitStart waits for a task in the spool to start.
	AwaitStart(ctx context.Context, id string, claimTimeout, startTimeout time.Duration) error
	// Attach follows a started task's log to stdout until it ends, and
	// fails if it failed. env is the task's, for the quota checks.
	// Interrupted, it ends the task too if abortOnCancel.
	Attach(ctx context.Context, id string, env map[string]string, abortOnCancel bool) error
	// Log writes the task's log so far to w.
	Log(ctx context.Context, id string, w io.Writer) error
	// ReadFile is a file in the task's directory; os.ErrNotExist when it
	// has none of that name.
	ReadFile(ctx context.Context, id, name string) ([]byte, error)
	WriteFile(ctx context.Context, id, name string, data []byte) error
	// Files names the files in the task's directory.
	Files(ctx context.Context, id string) ([]string, error)
	Close()
}

// Connect reaches the tasks in a sandbox, waking it if it sleeps: through
// its task server, or through envd if its image predates one.
func Connect(ctx context.Context, namespace, name string) (Sandbox, error) {
	if os.Getenv(EnvTransport) != "envd" {
		sb, err := ConnectServer(ctx, namespace, name)
		if err == nil {
			fmt.Fprintf(os.Stderr, "Connected to sandbox %s's task server.\n", name)
			return sb, nil
		}
		fmt.Fprintf(os.Stderr, "Sandbox %s has no task server to use (%v); using envd.\n", name, err)
	}
	c, err := envd.Connect(ctx, namespace, name)
	if err != nil {
		return nil, err
	}
	return &EnvdSandbox{name: name, Client: c}, nil
}

// ConnectServer reaches a sandbox's task server, failing if it has none
// this factory can use.
func ConnectServer(ctx context.Context, namespace, name string) (*ServerSandbox, error) {
	fmt.Fprintf(os.Stderr, "Waiting for sandbox pod %s to become ready...\n", name)
	pod, err := envd.GetSandboxPodName(ctx, namespace, name)
	if err != nil {
		return nil, err
	}
	kc, err := clients.NewKubernetesClient()
	if err != nil {
		return nil, err
	}
	s := &ServerSandbox{name: name, namespace: namespace, kc: kc}
	if err := s.open(ctx, pod); err != nil {
		return nil, err
	}
	vctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	v, err := s.client().Version(vctx)
	if err != nil {
		s.Close()
		return nil, err
	}
	if v < APIVersion {
		s.Close()
		return nil, fmt.Errorf("its task server serves version %d, this factory needs %d", v, APIVersion)
	}
	return s, nil
}

// ServerSandbox reaches a sandbox's tasks through its task server.
type ServerSandbox struct {
	name, namespace string
	kc              *clients.KubernetesClient

	mu  sync.Mutex
	fwd *forward
	cli *Client
}

func (s *ServerSandbox) Name() string      { return s.name }
func (s *ServerSandbox) Transport() string { return "task server" }

func (s *ServerSandbox) open(ctx context.Context, pod string) error {
	f, err := openForward(ctx, s.kc, s.namespace, pod)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fwd != nil {
		s.fwd.close()
	}
	// A transport of its own, so no connection outlives its forward.
	s.fwd, s.cli = f, NewClient(f.baseURL(), &http.Client{Transport: &http.Transport{}})
	return nil
}

func (s *ServerSandbox) client() *Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cli
}

func (s *ServerSandbox) forwarding() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fwd != nil && s.fwd.alive()
}

// reconnect opens a new forward, to the sandbox's pod as it is now.
func (s *ServerSandbox) reconnect(ctx context.Context) error {
	pod, err := envd.GetSandboxPodName(ctx, s.namespace, s.name)
	if err != nil {
		return err
	}
	return s.open(ctx, pod)
}

// call runs fn, and once more over a new forward if the first try did
// not reach the server. Every call is safe to repeat: a post is
// idempotent by its task's id.
func (s *ServerSandbox) call(ctx context.Context, fn func(*Client) error) error {
	if !s.forwarding() {
		// The forward ended with its connection, when the pod went, say.
		if err := s.reconnect(ctx); err != nil {
			return err
		}
	}
	err := fn(s.client())
	var se *StatusError
	if err == nil || errors.As(err, &se) || errors.Is(err, os.ErrNotExist) || ctx.Err() != nil {
		return err
	}
	klog.Warningf("Task server connection broke (%v); reconnecting...", err)
	if rerr := s.reconnect(ctx); rerr != nil {
		return fmt.Errorf("%w (reconnecting: %v)", err, rerr)
	}
	return fn(s.client())
}

func (s *ServerSandbox) List(ctx context.Context) ([]spool.Entry, error) {
	var entries []spool.Entry
	err := s.call(ctx, func(c *Client) (err error) {
		entries, err = c.List(ctx)
		return err
	})
	return entries, err
}

func (s *ServerSandbox) get(ctx context.Context, id string) (spool.Entry, error) {
	var e spool.Entry
	err := s.call(ctx, func(c *Client) (err error) {
		e, err = c.Get(ctx, id)
		return err
	})
	return e, err
}

func (s *ServerSandbox) Start(ctx context.Context, task spool.Task, recipe []byte, inputs, env map[string]string) error {
	var resp PostResponse
	err := s.call(ctx, func(c *Client) (err error) {
		resp, err = c.Post(ctx, PostRequest{Task: task, Recipe: recipe, Inputs: inputs, Env: env})
		return err
	})
	if err != nil {
		return fmt.Errorf("starting task %s: %w", task.ID, err)
	}
	if resp.Task.ID != task.ID {
		return fmt.Errorf("run name %s is task %s already", task.RunName, resp.Task.ID)
	}
	return nil
}

// AwaitStart waits for a task a client left in the spool: the server's
// daemon claims it, so only a task claimed and never started fails.
func (s *ServerSandbox) AwaitStart(ctx context.Context, id string, _, startTimeout time.Duration) error {
	deadline := time.Now().Add(startTimeout)
	for {
		e, err := s.get(ctx, id)
		if err != nil {
			return err
		}
		switch e.State {
		case spool.Running, spool.Exited:
			return nil
		case spool.Claimed:
			if time.Now().After(deadline) {
				return fmt.Errorf("task %s was claimed but has not started after %s", id, startTimeout)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// quotaError is a fatal quota or suspension error read in a task's log.
type quotaError struct{ err error }

func (e *quotaError) Error() string { return e.err.Error() }

// Attach follows the task's log in one stream, resuming where it broke
// off, and reads its exit code once the stream ends with it.
func (s *ServerSandbox) Attach(ctx context.Context, id string, env map[string]string, abortOnCancel bool) error {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigChan)
	loopCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-sigChan:
			fmt.Printf("\nInterrupt received. Aborting task in sandbox pod...\n")
			cancel()
		case <-loopCtx.Done():
		}
	}()

	end := func(kill bool) {
		killCtx, killCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer killCancel()
		if err := s.call(killCtx, func(c *Client) error {
			_, err := c.Cancel(killCtx, id, kill)
			return err
		}); err != nil {
			klog.Errorf("Ending task %s: %v", id, err)
		}
	}

	quota := envd.NewQuotaWatch(env)
	var offset int64
	broken := 0
	for {
		err := s.follow(loopCtx, id, &offset, quota)
		var qe *quotaError
		switch {
		case loopCtx.Err() != nil:
			if abortOnCancel {
				fmt.Printf("Terminating process in pod...\n")
				end(false)
			}
			return loopCtx.Err()
		case errors.As(err, &qe):
			klog.Warningf("Fatal quota/suspension error detected in task output. Terminating task process group in sandbox pod immediately...")
			end(true)
			return qe.err
		case err != nil:
			var se *StatusError
			if errors.As(err, &se) || broken >= 30 {
				return fmt.Errorf("following task %s: %w", id, err)
			}
			broken++
			klog.Warningf("Log stream broke (%v). Reconnecting...", err)
			if rerr := s.reconnect(loopCtx); rerr != nil {
				klog.Errorf("Failed to reconnect to the task server: %v", rerr)
			}
			sleep(loopCtx, 2*time.Second)
			continue
		}
		e, err := s.get(loopCtx, id)
		if err != nil {
			if loopCtx.Err() == nil && broken < 30 {
				broken++
				sleep(loopCtx, 2*time.Second)
				continue
			}
			return fmt.Errorf("reading task %s: %w", id, err)
		}
		if e.State != spool.Exited {
			// The stream ended before the task did.
			sleep(loopCtx, time.Second)
			continue
		}
		code, convErr := strconv.Atoi(e.ExitCode)
		if err := quota.Final(nil, convErr != nil || code != 0); err != nil {
			end(true)
			return err
		}
		if convErr != nil {
			return fmt.Errorf("invalid exit code '%s': %w", e.ExitCode, convErr)
		}
		if code != 0 {
			return fmt.Errorf("task failed with exit code %d", code)
		}
		return nil
	}
}

// follow copies the task's log from *offset to stdout until the server
// ends it, which it does once the task has ended.
func (s *ServerSandbox) follow(ctx context.Context, id string, offset *int64, quota *envd.QuotaWatch) error {
	body, err := s.client().Log(ctx, id, *offset, true)
	if err != nil {
		return err
	}
	defer body.Close()
	buf := make([]byte, 32<<10)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			_, _ = os.Stdout.Write(buf[:n])
			*offset += int64(n)
			if qerr := quota.Poll(buf[:n]); qerr != nil {
				return &quotaError{qerr}
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func (s *ServerSandbox) Log(ctx context.Context, id string, w io.Writer) error {
	return s.call(ctx, func(c *Client) error {
		body, err := c.Log(ctx, id, 0, false)
		if err != nil {
			return err
		}
		defer body.Close()
		_, err = io.Copy(w, body)
		return err
	})
}

func (s *ServerSandbox) ReadFile(ctx context.Context, id, name string) ([]byte, error) {
	var data []byte
	err := s.call(ctx, func(c *Client) (err error) {
		data, err = c.ReadFile(ctx, id, name)
		return err
	})
	return data, err
}

func (s *ServerSandbox) WriteFile(ctx context.Context, id, name string, data []byte) error {
	return s.call(ctx, func(c *Client) error { return c.WriteFile(ctx, id, name, data) })
}

func (s *ServerSandbox) Files(ctx context.Context, id string) ([]string, error) {
	var names []string
	err := s.call(ctx, func(c *Client) (err error) {
		names, err = c.Files(ctx, id)
		return err
	})
	return names, err
}

func (s *ServerSandbox) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fwd != nil {
		s.fwd.close()
	}
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// EnvdSandbox reaches a sandbox's tasks through envd: shell commands for
// everything, the spool for starting recipes.
type EnvdSandbox struct {
	name string
	*envd.Client
}

func (s *EnvdSandbox) Name() string      { return s.name }
func (s *EnvdSandbox) Transport() string { return "envd" }

func (s *EnvdSandbox) List(ctx context.Context) ([]spool.Entry, error) {
	return spool.List(ctx, s.Client)
}

func (s *EnvdSandbox) Start(ctx context.Context, task spool.Task, recipe []byte, inputs, env map[string]string) error {
	if err := spool.Submit(ctx, s.Client, task, recipe, inputs, env); err != nil {
		return err
	}
	fmt.Printf("Spooled task %s; waiting for the sandbox to start it...\n", task.ID)
	return spool.AwaitStart(ctx, s.Client, task.ID, 20*time.Second, 2*time.Minute)
}

func (s *EnvdSandbox) AwaitStart(ctx context.Context, id string, claimTimeout, startTimeout time.Duration) error {
	return spool.AwaitStart(ctx, s.Client, id, claimTimeout, startTimeout)
}

func (s *EnvdSandbox) Attach(ctx context.Context, id string, env map[string]string, abortOnCancel bool) error {
	return s.AttachTask(ctx, spool.TaskDir(id), env, abortOnCancel)
}

func (s *EnvdSandbox) Log(ctx context.Context, id string, w io.Writer) error {
	return s.Exec(ctx, "cat "+envd.NewTaskFiles(spool.TaskDir(id)).LogFile+" 2>/dev/null", "/workspaces", nil, nil, w, os.Stderr)
}

func (s *EnvdSandbox) ReadFile(ctx context.Context, id, name string) ([]byte, error) {
	path := spool.TaskDir(id) + "/" + name
	var out, errOut bytes.Buffer
	if err := s.Exec(ctx, fmt.Sprintf("if [ -f %[1]s ]; then cat %[1]s; else echo missing >&2; fi", path), "/workspaces", nil, nil, &out, &errOut); err != nil {
		return nil, fmt.Errorf("reading %s: %w (stderr: %s)", path, err, strings.TrimSpace(errOut.String()))
	}
	if strings.TrimSpace(errOut.String()) == "missing" {
		return nil, fmt.Errorf("%w: %s", os.ErrNotExist, path)
	}
	return out.Bytes(), nil
}

func (s *EnvdSandbox) WriteFile(ctx context.Context, id, name string, data []byte) error {
	return s.Client.WriteFile(ctx, spool.TaskDir(id)+"/"+name, data)
}

func (s *EnvdSandbox) Files(ctx context.Context, id string) ([]string, error) {
	var out bytes.Buffer
	if err := s.Exec(ctx, "ls -1 "+spool.TaskDir(id), "/workspaces", nil, nil, &out, nil); err != nil {
		return nil, err
	}
	return strings.Fields(out.String()), nil
}

func (s *EnvdSandbox) Close() { s.Client.Close() }
