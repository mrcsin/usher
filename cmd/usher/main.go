// Command usher keeps AmneziaWG peers and client configs in step with config/usher.yml.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	awgv1 "github.com/mrcsin/awg-grpc/gen/awg/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/mrcsin/usher/internal/pass"
	"github.com/mrcsin/usher/internal/watch"
)

const socketTarget = "unix:///run/awg-grpc/awg.sock"

// version is set at build time with -X main.version.
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Getenv, os.Stderr))
}

func run(ctx context.Context, args []string, getenv func(string) string, stderr io.Writer) int {
	if len(args) != 1 || args[0] != "run" {
		fmt.Fprintln(stderr, "usage: usher run")
		return 2
	}
	settings, err := settingsFrom(getenv)
	if err != nil {
		fmt.Fprintln(stderr, "usher:", err)
		return 1
	}

	return serve(ctx, settings, dialSocket, stderr)
}

func serve(ctx context.Context, settings pass.Settings, dial pass.Dial, stderr io.Writer) int {
	log := slog.New(slog.NewTextHandler(stderr, nil))
	log.Info("usher starting", "version", version)
	p := pass.New(settings, dial, log)
	intervals := watch.Intervals{Poll: 3 * time.Second, Settle: 500 * time.Millisecond, Refill: 30 * time.Second}
	watch.New(settings.ConfigPath, intervals, p.Run).Run(ctx)
	log.Info("usher stopped")
	return 0
}

func dialSocket() (awgv1.ManagementServiceClient, func(), error) {
	conn, err := grpc.NewClient(socketTarget, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, fmt.Errorf("opening %s: %w", socketTarget, err)
	}
	return awgv1.NewManagementServiceClient(conn), func() { _ = conn.Close() }, nil
}
