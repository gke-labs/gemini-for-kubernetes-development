package commands

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/tokenusage"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

func NewTokenDaemonCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "token-daemon",
		Short:  "Run the token-usage collector daemon that durably records gemini-cli usage pushed by factory tasks",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("token-daemon command does not take any arguments")
			}
			return runTokenDaemon(cmd.Context())
		},
	}
	return cmd
}

func runTokenDaemon(ctx context.Context) error {
	log := klog.FromContext(ctx)

	storageRoot := os.Getenv("STORAGE_ROOT")
	if storageRoot == "" {
		storageRoot = "/data"
	}

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}

	// GCS_BUCKET turns on the archive tier: the PVC's logs are flushed to
	// gs://$GCS_BUCKET/$GCS_PREFIX every FLUSH_INTERVAL, and replayed from
	// there at startup.
	var archive tokenusage.Archive
	bucket := os.Getenv("GCS_BUCKET")
	if bucket != "" {
		prefix := os.Getenv("GCS_PREFIX")
		if prefix == "" {
			prefix = "token-usage"
		}
		a, err := tokenusage.NewGCSArchive(ctx, bucket, prefix)
		if err != nil {
			return err
		}
		archive = a
		log.Info("Archiving token usage to GCS", "bucket", bucket, "prefix", prefix)
	}
	flushInterval := 5 * time.Minute
	if v := os.Getenv("FLUSH_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("FLUSH_INTERVAL: %w", err)
		}
		flushInterval = d
	}

	store, err := tokenusage.OpenStore(ctx, storageRoot, archive)
	if err != nil {
		return fmt.Errorf("opening token-usage store: %w", err)
	}
	defer store.Close()

	srv := &http.Server{Addr: addr, Handler: tokenusage.NewServerForStore(store).Handler()}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()
	if archive != nil {
		go store.RunFlusher(ctx, flushInterval)
	}

	log.Info("Starting token-usage collector", "addr", addr, "storageRoot", storageRoot)
	select {
	case err := <-serveErr:
		return fmt.Errorf("token-usage collector exited: %w", err)
	case <-ctx.Done():
	}

	// Stop taking writes, then flush what they left on the PVC, so a clean
	// shutdown (a rollout) leaves nothing behind but the archive.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error(err, "Shutting down the token-usage collector")
	}
	if err := store.Flush(shutdownCtx); err != nil {
		log.Error(err, "Final flush to the archive; the PVC keeps what was not uploaded")
	}
	return nil
}
