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
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/mrcsin/usher/gen/xray/app/proxyman/command"
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
	return serve(ctx, env.Settings, backendsFor(env, log), log)
}

func backendsFor(env environment, log *slog.Logger) []pass.Backend {
	var backends []pass.Backend
	if env.AWGSocket != "" {
		backends = append(backends, awgBackend(env, log))
	}
	if env.XraySocket != "" {
		backends = append(backends, xrayBackend(env, log))
	}
	return backends
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
	dial := dialSocket(env.AWGSocket)
	return pass.Backend{
		Name:   "awg-grpc",
		Suffix: awg.Suffix,
		Open: func(ctx context.Context) (pass.Session, error) {
			session, err := awg.Open(ctx, dial, env.Host, env.DNS, log)
			if err != nil {
				return nil, err
			}
			return session, nil
		},
	}
}

func xrayBackend(env environment, log *slog.Logger) pass.Backend {
	dial := dialXraySocket(env.XraySocket)
	return pass.Backend{
		Name:   "xray",
		Suffix: xray.Suffix,
		Open: func(ctx context.Context) (pass.Session, error) {
			session, err := xray.Open(ctx, dial, env.Host, log)
			if err != nil {
				return nil, err
			}
			return session, nil
		},
	}
}

func dialSocket(path string) awg.Dial {
	return func() (awgv1.ManagementServiceClient, func(), error) {
		conn, err := dialConn(path)
		if err != nil {
			return nil, nil, err
		}
		return awgv1.NewManagementServiceClient(conn), func() { _ = conn.Close() }, nil
	}
}

func dialXraySocket(path string) xray.Dial {
	return func() (command.HandlerServiceClient, func(), error) {
		conn, err := dialConn(path)
		if err != nil {
			return nil, nil, err
		}
		return command.NewHandlerServiceClient(conn), func() { _ = conn.Close() }, nil
	}
}

func dialConn(path string) (*grpc.ClientConn, error) {
	target := "unix://" + path
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", target, err)
	}
	return conn, nil
}
