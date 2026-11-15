// Command slack-sink stands in for Slack incoming webhooks during local
// testing: point SLACK_WEBHOOK_URL at it and open its web page to read the
// triage reports.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"k8s-agent-sandbox/internal/cli"
	"k8s-agent-sandbox/internal/slacksink"
)

func main() { cli.Main(newRootCmd()) }

func newRootCmd() *cobra.Command {
	var addr string
	cmd := cli.Command("slack-sink", "Local stand-in for Slack incoming webhooks",
		func(cmd *cobra.Command, log *slog.Logger) error { return run(cmd.Context(), log, addr) })
	cmd.Flags().StringVar(&addr, "listen-addr", ":8080", "HTTP listen address")
	return cmd
}

func run(ctx context.Context, log *slog.Logger, addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           slacksink.New(200).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", addr)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
