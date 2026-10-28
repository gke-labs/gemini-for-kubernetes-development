package taskapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/spool"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskfiles"
	"k8s.io/klog/v2"
)

// Sandbox is the tasks in one sandbox, reached through its task server.
type Sandbox interface {
	Name() string
	// List is every task, newest first.
	List(ctx context.Context) ([]spool.Entry, error)
	// Start hands the sandbox a recipe task and returns once it has
	// started. spool.ErrNotClaimed: the sandbox has no daemon to start it,
	// and nothing of it runs. secrets go to the task outside its
	// environment (PostRequest.Secrets); a sandbox that cannot keep them
	// so refuses them, and nothing of the task runs. files, if any, are in
	// the task's directory before it starts; a sandbox that cannot take
	// them refuses the task.
	Start(ctx context.Context, task spool.Task, recipe, source []byte, inputs, env, secrets map[string]string, files *Files) error
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
	// AllFiles is Files with the files one directory down, as
	// "<dir>/<name>".
	AllFiles(ctx context.Context, id string) ([]string, error)
	Close()
}

// Connect reaches the tasks in a sandbox, waking it if it sleeps, through
// its task server. A sandbox whose image has none is recreated, not
// served another way.
func Connect(ctx context.Context, namespace, name string) (Sandbox, error) {
	sb, err := ConnectServer(ctx, namespace, name)
	if err != nil {
		return nil, fmt.Errorf("sandbox %s has no task server to use (%w); recreate it on a current image", name, err)
	}
	fmt.Fprintf(os.Stderr, "Connected to sandbox %s's task server.\n", name)
	return sb, nil
}

// ConnectServer reaches a sandbox's task server, failing if it has none
// this factory can use.
func ConnectServer(ctx context.Context, namespace, name string) (*ServerSandbox, error) {
	fmt.Fprintf(os.Stderr, "Waiting for sandbox pod %s to become ready...\n", name)
	pod, err := k8s.SandboxPod(ctx, namespace, name)
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
	v, err := awaitServer(ctx, serverStartTimeout, serverRetryEvery,
		func(ctx context.Context) (Version, error) {
			vctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			return s.client().Version(vctx)
		},
		s.reconnect)
	if err != nil {
		s.Close()
		return nil, err
	}
	if v.API < APIVersion {
		s.Close()
		return nil, fmt.Errorf("its task server serves version %d, this factory needs %d", v.API, APIVersion)
	}
	s.secrets = v.Secrets >= SecretsVersion
	s.uploads = v.Uploads >= UploadsVersion
	return s, nil
}

// A pod is ready once its container runs, before factory daemon in it
// listens: a sandbox just woken (or started) refuses its task server's
// port for some seconds. ConnectServer asks again for serverStartTimeout,
// every serverRetryEvery, before it says the sandbox has none.
var (
	serverStartTimeout = 90 * time.Second
	serverRetryEvery   = 2 * time.Second
)

// awaitServer asks the task server its version until it answers or
// timeout passes, opening a new forward (reopen) between tries: a forward
// opened while nothing listened is not reused. The last error is returned.
func awaitServer(ctx context.Context, timeout, every time.Duration, version func(context.Context) (Version, error), reopen func(context.Context) error) (Version, error) {
	deadline := time.Now().Add(timeout)
	for tries := 1; ; tries++ {
		v, err := version(ctx)
		if err == nil {
			return v, nil
		}
		if ctx.Err() != nil || time.Now().Add(every).After(deadline) {
			return Version{}, err
		}
		if tries == 1 {
			fmt.Fprintf(os.Stderr, "Its task server does not answer yet (%v); asking again for up to %s...\n", err, timeout)
		}
		select {
		case <-ctx.Done():
			return Version{}, err
		case <-time.After(every):
		}
		if rerr := reopen(ctx); rerr != nil {
			klog.V(2).Infof("reopening the forward to the task server: %v", rerr)
		}
	}
}

// errNoSecrets is a sandbox whose image predates task secrets.
func errNoSecrets(name string) error {
	return fmt.Errorf("sandbox %s's image cannot keep a task's GitHub token out of its environment; recreate the sandbox", name)
}

// ServerSandbox reaches a sandbox's tasks through its task server.
type ServerSandbox struct {
	name, namespace string
	kc              *clients.KubernetesClient

	// secrets: the server keeps a task's secrets out of its environment.
	secrets bool
	// uploads: the server takes files for a task (PostRequest.Upload).
	uploads bool

	mu  sync.Mutex
	fwd *forward
	cli *Client
}

func (s *ServerSandbox) Name() string { return s.name }

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
	pod, err := k8s.SandboxPod(ctx, s.namespace, s.name)
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

func (s *ServerSandbox) Start(ctx context.Context, task spool.Task, recipe, source []byte, inputs, env, secrets map[string]string, files *Files) error {
	if len(secrets) > 0 && !s.secrets {
		return errNoSecrets(s.name)
	}
	req := PostRequest{Task: task, Recipe: recipe, Source: source, Inputs: inputs, Env: env, Secrets: secrets}
	if files != nil {
		if !s.uploads {
			return fmt.Errorf("sandbox %s's image cannot take files for a task; recreate the sandbox", s.name)
		}
		// Uploaded under the task's id: a retry replaces it.
		if err := s.call(ctx, func(c *Client) error { return c.Upload(ctx, task.ID, files.Dir) }); err != nil {
			return fmt.Errorf("uploading %s for task %s: %w", files.Dir, task.ID, err)
		}
		req.Upload, req.UploadTo = task.ID, files.To
	}
	var resp PostResponse
	err := s.call(ctx, func(c *Client) (err error) {
		resp, err = c.Post(ctx, req)
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

	quota := taskfiles.NewQuotaWatch(env)
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
func (s *ServerSandbox) follow(ctx context.Context, id string, offset *int64, quota *taskfiles.QuotaWatch) error {
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
	return s.log(ctx, id, w, false)
}

// FollowLog is Log, going on until the task ends.
func (s *ServerSandbox) FollowLog(ctx context.Context, id string, w io.Writer) error {
	return s.log(ctx, id, w, true)
}

func (s *ServerSandbox) log(ctx context.Context, id string, w io.Writer, follow bool) error {
	return s.call(ctx, func(c *Client) error {
		body, err := c.Log(ctx, id, 0, follow)
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

func (s *ServerSandbox) AllFiles(ctx context.Context, id string) ([]string, error) {
	var names []string
	err := s.call(ctx, func(c *Client) (err error) {
		names, err = c.AllFiles(ctx, id)
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
