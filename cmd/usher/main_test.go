package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/proxyman/command"
	statscmd "github.com/xtls/xray-core/app/stats/command"
	"google.golang.org/grpc"

	"github.com/mrcsin/usher/internal/pass"
	"github.com/mrcsin/usher/internal/status"
)

func TestRun(t *testing.T) {
	validEnv := map[string]string{"USHER_HOST": "203.0.113.10", "USHER_AWG_SOCKET": "/run/awg.sock", "USHER_DNS": "1.1.1.1"}
	tests := []struct {
		name       string
		args       []string
		env        map[string]string
		wantCode   int
		wantStderr string
	}{
		{name: "no argument", args: nil, env: validEnv, wantCode: 2, wantStderr: "usage"},
		{name: "unknown argument", args: []string{"serve"}, env: validEnv, wantCode: 2, wantStderr: "usage"},
		{name: "extra argument", args: []string{"run", "x"}, env: validEnv, wantCode: 2, wantStderr: "usage"},
		{name: "missing host", args: []string{"run"}, env: map[string]string{"USHER_AWG_SOCKET": "/run/awg.sock", "USHER_DNS": "1.1.1.1"}, wantCode: 1, wantStderr: "USHER_HOST"},
		{name: "bad dns", args: []string{"run"}, env: map[string]string{"USHER_HOST": "203.0.113.10", "USHER_AWG_SOCKET": "/run/awg.sock", "USHER_DNS": "dns.example"}, wantCode: 1, wantStderr: "USHER_DNS"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			time.AfterFunc(200*time.Millisecond, cancel)

			var stderr bytes.Buffer
			done := make(chan int, 1)
			go func() {
				done <- run(ctx, tt.args, func(k string) string { return tt.env[k] }, &stderr)
			}()
			select {
			case code := <-done:
				if code != tt.wantCode {
					t.Errorf("exit code = %d, want %d; stderr: %s", code, tt.wantCode, stderr.String())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("run did not return")
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}

func TestBackends(t *testing.T) {
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	env := environment{
		AWGSocket:  "/run/awg.sock",
		XraySocket: "/run/xray/api.sock",
		XrayConfig: filepath.Join(t.TempDir(), "missing.json"),
	}

	awg := awgBackend(env, log)
	if awg.Name != "awg-grpc" || !slices.Equal(awg.Suffixes, []string{".conf"}) {
		t.Errorf("awg backend = %q %v", awg.Name, awg.Suffixes)
	}

	xr := xrayBackend(env, log)
	if xr.Name != "xray" || !slices.Equal(xr.Suffixes, []string{".json", ".txt"}) {
		t.Errorf("xray backend = %q %v", xr.Name, xr.Suffixes)
	}
	session, err := xr.Open(context.Background())
	if err == nil || session != nil {
		t.Errorf("Open = %v, %v, want a nil session and an error for the missing config", session, err)
	}
}

type fakeHandlerServer struct {
	command.UnimplementedHandlerServiceServer
}

func (fakeHandlerServer) ListInbounds(context.Context, *command.ListInboundsRequest) (*command.ListInboundsResponse, error) {
	return &command.ListInboundsResponse{}, nil
}

type fakeStatsServer struct {
	statscmd.UnimplementedStatsServiceServer
}

func (fakeStatsServer) QueryStats(context.Context, *statscmd.QueryStatsRequest) (*statscmd.QueryStatsResponse, error) {
	return &statscmd.QueryStatsResponse{Stat: []*statscmd.Stat{{Name: "user>>>in/alice>>>traffic>>>uplink", Value: 5}}}, nil
}

func (fakeStatsServer) GetAllOnlineUsers(context.Context, *statscmd.GetAllOnlineUsersRequest) (*statscmd.GetAllOnlineUsersResponse, error) {
	return &statscmd.GetAllOnlineUsersResponse{Users: []string{"user>>>in/alice>>>online"}}, nil
}

// TestXrayBackendDialsBothServices runs the dial closure of the xray backend against a gRPC server
// on a unix socket: Open calls HandlerService and Stats calls StatsService.
func TestXrayBackendDialsBothServices(t *testing.T) {
	// A short directory: macOS caps a unix socket path at 104 bytes.
	dir, err := os.MkdirTemp("", "usher")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "api.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	command.RegisterHandlerServiceServer(server, fakeHandlerServer{})
	statscmd.RegisterStatsServiceServer(server, fakeStatsServer{})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	config := filepath.Join(dir, "config.json")
	const xrayConfig = `{"inbounds": [], "stats": {}, "api": {"services": ["HandlerService", "StatsService"]},
		"policy": {"levels": {"0": {"statsUserUplink": true, "statsUserDownlink": true, "statsUserOnline": true}}}}`
	if err := os.WriteFile(config, []byte(xrayConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	env := environment{XraySocket: socket, XrayConfig: config}
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))

	session, err := xrayBackend(env, log).Open(t.Context())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer session.Close()
	got, err := session.Stats(t.Context(), nil, "in", []string{"alice"})
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if want := map[string]status.UserStats{"alice": {Uplink: 5, Online: true}}; !maps.Equal(got, want) {
		t.Errorf("Stats = %v, want %v", got, want)
	}
}

func TestServe(t *testing.T) {
	root := t.TempDir()
	settings := pass.Settings{
		ConfigPath: filepath.Join(root, "usher.yml"),
		ClientsDir: filepath.Join(root, "clients"),
		StatePath:  filepath.Join(root, "users.json"),
	}
	backends := []pass.Backend{{
		Name:     "fake",
		Suffixes: []string{".conf"},
		Open:     func(context.Context) (pass.Session, error) { return nil, errors.New("no socket") },
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(200*time.Millisecond, cancel)

	var stderr bytes.Buffer
	log := slog.New(slog.NewTextHandler(&stderr, nil))
	done := make(chan int, 1)
	go func() { done <- serve(ctx, settings, backends, log) }()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit code = %d, want 0; stderr: %s", code, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return")
	}

	for _, want := range []string{"usher starting", "version=dev", "usher stopped"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr = %q, want it to contain %q", stderr.String(), want)
		}
	}
}
