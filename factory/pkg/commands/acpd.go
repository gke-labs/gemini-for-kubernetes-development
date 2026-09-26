package commands

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/acpd"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

// EnvACPDPort overrides the listen port.
const EnvACPDPort = "ACPD_PORT"

// NewACPDCommand serves the Agent Client Protocol over HTTP.
//
//	factory acpd [--port 49984] [--cwd /workspaces/<repo>]
//
// acpd holds a conversation open with an agent CLI: it owns the engine
// process, speaks ACP to it over stdio, and exposes sessions as HTTP —
// create, prompt, follow the transcript, answer a permission prompt,
// cancel. That is what makes a research session a chat in a browser
// instead of a tmux window someone is scrolling.
//
// It runs inside the sandbox, started by `factory daemon` when
// ACPD_ENABLE is set. Running it directly is how you test against a local
// `gemini --acp` without a cluster:
//
//	factory acpd --cwd ~/src/myrepo
//	curl -XPOST localhost:49984/sessions -H 'X-Engine-Api-Key: …' \
//	     -d '{"id":"local","engine":"gemini"}'
//	curl -N localhost:49984/sessions/local/events
func NewACPDCommand(ctx context.Context) *cobra.Command {
	var port int
	var stateDir, cwd string

	cmd := &cobra.Command{
		Use:   "acpd",
		Short: "Serve agent conversations over HTTP (Agent Client Protocol)",
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("acpd does not take any arguments")
			}
			if cwd == "" {
				wd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("resolving working directory: %w", err)
				}
				cwd = wd
			}
			return RunACPD(c.Context(), port, stateDir, cwd)
		},
	}

	cmd.Flags().IntVar(&port, "port", acpd.DefaultPort, "Port to listen on")
	cmd.Flags().StringVar(&stateDir, "state-dir", acpd.StateDirFromEnv(), "Where session transcripts are written")
	cmd.Flags().StringVar(&cwd, "cwd", "", "Working directory for agent processes (default: current directory)")

	return cmd
}

// RunACPD serves until ctx ends, then stops accepting and ends every
// session. Exported so `factory daemon` can start it in-process rather
// than shelling out to a second copy of this binary.
func RunACPD(ctx context.Context, port int, stateDir, cwd string) error {
	log := klog.FromContext(ctx)

	server := acpd.NewServer(stateDir, cwd)
	httpServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: server.Handler(),
		// No write timeout: the event stream is a long-lived response by
		// design, and a deadline here would cut every conversation at a
		// fixed length.
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		log.Info("acpd shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
		server.Close()
	}()

	log.Info("acpd listening", "port", port, "stateDir", stateDir, "cwd", cwd)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("acpd listen: %w", err)
	}
	return nil
}

// acpdPortFromEnv reads ACPD_PORT, falling back to the default when unset
// or unparseable.
func acpdPortFromEnv(ctx context.Context) int {
	raw := os.Getenv(EnvACPDPort)
	if raw == "" {
		return acpd.DefaultPort
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port <= 0 || port > 65535 {
		klog.FromContext(ctx).Info("ignoring invalid "+EnvACPDPort, "value", raw, "using", acpd.DefaultPort)
		return acpd.DefaultPort
	}
	return port
}
