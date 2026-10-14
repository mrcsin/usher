package xray

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xtls/xray-core/app/proxyman/command"
	"github.com/xtls/xray-core/core"
	"google.golang.org/grpc"
)

// fakeHandlerService answers ListInbounds only; any other call panics on the nil embedded client.
type fakeHandlerService struct {
	command.HandlerServiceClient
	inbounds []*core.InboundHandlerConfig
	err      error
}

func (f *fakeHandlerService) ListInbounds(_ context.Context, _ *command.ListInboundsRequest,
	_ ...grpc.CallOption) (*command.ListInboundsResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &command.ListInboundsResponse{Inbounds: f.inbounds}, nil
}

// driftConfig holds the parts of the test server config that the cases change.
type driftConfig struct {
	dir           string
	realityPort   int
	realityKey    string
	shortID       string
	realityProto  string
	certContent   string
	hysteriaCerts string
	extra         string // extra inbound objects, each followed by a comma
}

func newDriftConfig(dir string) driftConfig {
	return driftConfig{
		dir:           dir,
		realityPort:   443,
		realityKey:    testKey(1),
		shortID:       "ab12",
		realityProto:  "vless",
		certContent:   "first-certificate",
		hysteriaCerts: `[{"certificate": ["inline-cert"], "key": ["inline-key"]}]`,
	}
}

// testKey returns a valid Reality private key: 32 bytes of fill in base64url.
func testKey(fill byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32))
}

func (c driftConfig) write(t *testing.T) string {
	t.Helper()
	for name, content := range map[string]string{"ws.pem": c.certContent, "ws.key": "ws-key"} {
		if err := os.WriteFile(filepath.Join(c.dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	doc := fmt.Sprintf(`{
  "inbounds": [
    %[8]s
    {"tag": "api-in", "protocol": "dokodemo-door", "port": 10085, "settings": {"address": "127.0.0.1"}},
    {"tag": "reality", "protocol": %[1]q, "port": %[2]d,
     "settings": {"clients": [], "decryption": "none"},
     "streamSettings": {"network": "raw", "security": "reality",
       "realitySettings": {"privateKey": %[3]q, "serverNames": ["www.example.com"],
         "shortIds": [%[4]q], "target": "www.example.com:443"}}},
    {"tag": "ws", "protocol": "vless", "port": 2083,
     "settings": {"clients": [], "decryption": "none"},
     "streamSettings": {"network": "ws", "security": "tls",
       "tlsSettings": {"serverName": "ws.example.com", "certificates": [
         {"certificateFile": %[5]q, "keyFile": %[6]q}]},
       "wsSettings": {"path": "/ws"}}},
    {"tag": "hy2", "protocol": "hysteria", "port": 8443,
     "settings": {"version": 2, "clients": []},
     "streamSettings": {"network": "hysteria", "security": "tls",
       "tlsSettings": {"serverName": "hy.example.com", "alpn": ["h3"], "certificates": %[7]s},
       "hysteriaSettings": {"version": 2}}}
  ]
}`, c.realityProto, c.realityPort, c.realityKey, c.shortID,
		filepath.Join(c.dir, "ws.pem"), filepath.Join(c.dir, "ws.key"), c.hysteriaCerts, c.extra)
	path := filepath.Join(c.dir, "config.json")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// runningFrom builds every inbound of the file at path the way Xray does at start.
func runningFrom(t *testing.T, path string) []*core.InboundHandlerConfig {
	t.Helper()
	inbounds, err := ReadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	var running []*core.InboundHandlerConfig
	for _, in := range inbounds {
		if in.Err != nil {
			t.Fatalf("%s: %v", in.Tag, in.Err)
		}
		built, err := in.Detour.Build()
		if err != nil {
			t.Fatalf("%s: %v", in.Tag, err)
		}
		running = append(running, built)
	}
	return running
}

func TestCheckDrift(t *testing.T) {
	tests := []struct {
		name    string
		change  func(c *driftConfig)
		running func(running []*core.InboundHandlerConfig) []*core.InboundHandlerConfig
		want    map[string][]string // tag to the substrings of its reason
	}{
		{name: "match"},
		{name: "port change", change: func(c *driftConfig) { c.realityPort = 8444 },
			want: map[string][]string{"reality": {"port"}}},
		{name: "shortIds change", change: func(c *driftConfig) { c.shortID = "cd34" },
			want: map[string][]string{"reality": {"stream settings"}}},
		{name: "privateKey change", change: func(c *driftConfig) { c.realityKey = testKey(2) },
			want: map[string][]string{"reality": {"stream settings"}}},
		{name: "protocol change", change: func(c *driftConfig) { c.realityProto = "vmess" },
			want: map[string][]string{"reality": {"proxy type"}}},
		{name: "inline certificate change",
			change: func(c *driftConfig) {
				c.hysteriaCerts = `[{"certificate": ["other-cert"], "key": ["inline-key"]}]`
			},
			want: map[string][]string{"hy2": {"stream settings"}}},
		{name: "two changed parts", change: func(c *driftConfig) {
			c.realityPort = 8444
			c.shortID = "cd34"
		}, want: map[string][]string{"reality": {"port", "stream settings"}}},
		{name: "certificate file contents change", change: func(c *driftConfig) { c.certContent = "second-certificate" }},
		{name: "tag missing at runtime",
			running: func(running []*core.InboundHandlerConfig) []*core.InboundHandlerConfig {
				var kept []*core.InboundHandlerConfig
				for _, r := range running {
					if r.Tag != "ws" {
						kept = append(kept, r)
					}
				}
				return kept
			},
			want: map[string][]string{"ws": {"not running"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := newDriftConfig(t.TempDir())
			running := runningFrom(t, base.write(t))
			if tt.running != nil {
				running = tt.running(running)
			}
			changed := base
			if tt.change != nil {
				tt.change(&changed)
			}
			inbounds, err := ReadConfig(changed.write(t))
			if err != nil {
				t.Fatal(err)
			}

			var logs bytes.Buffer
			drift, err := checkDrift(context.Background(), &fakeHandlerService{inbounds: running},
				slog.New(slog.NewTextHandler(&logs, nil)), inbounds)
			if err != nil {
				t.Fatal(err)
			}
			if len(drift) != len(tt.want) {
				t.Errorf("drift = %v, want tags %v", drift, tt.want)
			}
			for tag, substrings := range tt.want {
				reason := drift[tag]
				if reason == nil {
					t.Errorf("%s: no drift reported", tag)
					continue
				}
				for _, s := range substrings {
					if !strings.Contains(reason.Error(), s) {
						t.Errorf("%s: reason %q lacks %q", tag, reason, s)
					}
				}
				for _, secret := range []string{base.realityKey, changed.realityKey, changed.shortID,
					"inline-key", "other-cert", "ws-key"} {
					if strings.Contains(reason.Error(), secret) {
						t.Errorf("%s: reason quotes a value: %q", tag, reason)
					}
				}
			}
			if logs.Len() != 0 {
				t.Errorf("unexpected log: %s", logs.String())
			}
		})
	}
}

func TestCheckDriftExtraRuntimeTag(t *testing.T) {
	base := newDriftConfig(t.TempDir())
	stray := base
	stray.extra = `{"tag": "stray", "protocol": "vless", "port": 2999,
		"settings": {"clients": [], "decryption": "none"}},`
	running := runningFrom(t, stray.write(t))
	inbounds, err := ReadConfig(base.write(t))
	if err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	drift, err := checkDrift(context.Background(), &fakeHandlerService{inbounds: running},
		slog.New(slog.NewTextHandler(&logs, nil)), inbounds)
	if err != nil {
		t.Fatal(err)
	}
	if len(drift) != 0 {
		t.Errorf("drift = %v, want none", drift)
	}
	if !strings.Contains(logs.String(), "stray") {
		t.Errorf("log does not name the stray inbound: %q", logs.String())
	}
	if strings.Contains(logs.String(), "api-in") {
		t.Errorf("log names an inbound of an unmanaged protocol: %q", logs.String())
	}
}

func TestCheckDriftSkipsInvalidInbound(t *testing.T) {
	base := newDriftConfig(t.TempDir())
	running := runningFrom(t, base.write(t))
	inbounds, err := ReadConfig(base.write(t))
	if err != nil {
		t.Fatal(err)
	}
	inbounds[0].Err = errors.New("invalid")

	drift, err := checkDrift(context.Background(), &fakeHandlerService{inbounds: running},
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), inbounds)
	if err != nil {
		t.Fatal(err)
	}
	if len(drift) != 0 {
		t.Errorf("drift = %v, want none for an inbound that failed validation", drift)
	}
}

func TestCheckDriftListError(t *testing.T) {
	_, err := checkDrift(context.Background(), &fakeHandlerService{err: errors.New("unavailable")},
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), nil)
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Errorf("err = %v, want the list error wrapped", err)
	}
}
