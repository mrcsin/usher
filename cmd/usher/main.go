// Command usher keeps AmneziaWG and Xray users and client configs in step with config/usher.yml.
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
	"github.com/xtls/xray-core/app/proxyman/command"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/mrcsin/usher/internal/awg"
	"github.com/mrcsin/usher/internal/pass"
	"github.com/mrcsin/usher/internal/watch"
	"github.com/mrcsin/usher/internal/xray"
)

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
	env, err := environmentFrom(getenv)
	if err != nil {
		fmt.Fprintln(stderr, "usher:", err)
		return 1
	}

	log := slog.New(slog.NewTextHandler(stderr, nil))
	var backends []pass.Backend
	if env.AWGSocket != "" {
		backends = append(backends, awgBackend(env, log))
	}
	if env.XraySocket != "" {
		backends = append(backends, xrayBackend(env, log))
	}
	return serve(ctx, env.Settings, backends, log)
}

func serve(ctx context.Context, settings pass.Settings, backends []pass.Backend, log *slog.Logger) int {
	log.Info("usher starting", "version", version)
	p := pass.New(settings, backends, log)
	intervals := watch.Intervals{Poll: 3 * time.Second, Settle: 500 * time.Millisecond, Refill: 30 * time.Second}
	watch.New(settings.ConfigPath, intervals, p.Run).Run(ctx)
	log.Info("usher stopped")
	return 0
}

func awgBackend(env environment, log *slog.Logger) pass.Backend {
	dial := dialUnix(env.AWGSocket, awgv1.NewManagementServiceClient)
	return pass.Backend{
		Name:     "awg-grpc",
		Suffixes: []string{awg.Suffix},
		Open: func(ctx context.Context) (pass.Session, error) {
			return asSession(awg.Open(ctx, dial, env.Host, env.DNS, log))
		},
	}
}

func xrayBackend(env environment, log *slog.Logger) pass.Backend {
	dial := dialUnix(env.XraySocket, command.NewHandlerServiceClient)
	return pass.Backend{
		Name:     "xray",
		Suffixes: []string{xray.JSONSuffix, xray.LinkSuffix},
		Open: func(ctx context.Context) (pass.Session, error) {
			return asSession(xray.Open(ctx, dial, env.XraySocket, env.XrayConfig, env.Host.String(), log))
		},
	}
}

// asSession converts a backend session to pass.Session. On error it returns a nil interface, not
// an interface that holds a nil pointer.
func asSession[S pass.Session](session S, err error) (pass.Session, error) {
	if err != nil {
		return nil, err
	}
	return session, nil
}

// dialUnix returns a dial function that opens a gRPC client over the unix socket at path.
func dialUnix[C any](path string, newClient func(grpc.ClientConnInterface) C) func() (C, func(), error) {
	target := "unix://" + path
	return func() (C, func(), error) {
		conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			var zero C
			return zero, nil, fmt.Errorf("opening %s: %w", target, err)
		}
		return newClient(conn), func() { _ = conn.Close() }, nil
	}
}
