package main

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	awgv1 "github.com/mrcsin/awg-grpc/gen/awg/v1"

	"github.com/mrcsin/usher/internal/pass"
)

func TestRun(t *testing.T) {
	validEnv := map[string]string{"USHER_HOST": "203.0.113.10", "USHER_DNS": "1.1.1.1"}
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
		{name: "missing host", args: []string{"run"}, env: map[string]string{"USHER_DNS": "1.1.1.1"}, wantCode: 1, wantStderr: "USHER_HOST"},
		{name: "bad dns", args: []string{"run"}, env: map[string]string{"USHER_HOST": "203.0.113.10", "USHER_DNS": "dns.example"}, wantCode: 1, wantStderr: "USHER_DNS"},
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

func TestServe(t *testing.T) {
	root := t.TempDir()
	settings := pass.Settings{
		Host:       netip.MustParseAddr("203.0.113.10"),
		DNS:        []netip.Addr{netip.MustParseAddr("1.1.1.1")},
		ConfigPath: filepath.Join(root, "usher.yml"),
		ClientsDir: filepath.Join(root, "clients"),
		StatePath:  filepath.Join(root, "users.json"),
	}
	dial := func() (awgv1.ManagementServiceClient, func(), error) { return nil, nil, errors.New("no socket") }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(200*time.Millisecond, cancel)

	var stderr bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- serve(ctx, settings, dial, &stderr) }()
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
