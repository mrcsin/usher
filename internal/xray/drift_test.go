package xray

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf"
	xjson "github.com/xtls/xray-core/infra/conf/json"
)

type driftConfig struct {
	dir           string
	realityPort   int
	realityKey    string
	shortID       string
	realityProto  string
	certContent   string
	hysteriaCerts string
	realityExtra  string
	extra         string
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
    {"tag": "reality", "protocol": %[1]q, "port": %[2]d, %[9]s
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
		filepath.Join(c.dir, "ws.pem"), filepath.Join(c.dir, "ws.key"), c.hysteriaCerts, c.extra, c.realityExtra)
	path := filepath.Join(c.dir, "config.json")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// runningFrom builds the inbounds through Xray's own reader, without usher's normalization.
func runningFrom(t *testing.T, path string) []*core.InboundHandlerConfig {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var cfg conf.Config
	if err := json.NewDecoder(&xjson.Reader{Reader: f}).Decode(&cfg); err != nil {
		t.Fatal(err)
	}
	var running []*core.InboundHandlerConfig
	for i := range cfg.InboundConfigs {
		built, err := cfg.InboundConfigs[i].Build()
		if err != nil {
			t.Fatalf("%s: %v", cfg.InboundConfigs[i].Tag, err)
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
		want    map[string][]string
	}{
		{name: "match"},
		{name: "sniffing change", change: func(c *driftConfig) { c.realityExtra = `"sniffing": {"enabled": true},` },
			want: map[string][]string{"reality": {"receiver settings"}}},
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
			inbounds := readInbounds(t, changed.write(t))

			var logs bytes.Buffer
			drift, err := checkDrift(context.Background(), newUserService(running),
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
	inbounds := readInbounds(t, base.write(t))

	var logs bytes.Buffer
	drift, err := checkDrift(context.Background(), newUserService(running),
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
	inbounds := readInbounds(t, base.write(t))
	inbounds[0].Err = errors.New("invalid")

	drift, err := checkDrift(context.Background(), newUserService(running),
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), inbounds)
	if err != nil {
		t.Fatal(err)
	}
	if len(drift) != 0 {
		t.Errorf("drift = %v, want none for an inbound that failed validation", drift)
	}
}

func TestCheckDriftListError(t *testing.T) {
	_, err := checkDrift(context.Background(), &userService{inboundErr: errors.New("unavailable")},
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), nil)
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Errorf("err = %v, want the list error wrapped", err)
	}
}

// TestCheckDriftMatchesXrayBuild: a file without streamSettings and a file in aliases show no drift.
func TestCheckDriftMatchesXrayBuild(t *testing.T) {
	plain := newDriftConfig(t.TempDir())
	plain.extra = `{"tag": "plain", "protocol": "vless", "port": 9001,
		"settings": {"clients": [], "decryption": "none"}},`
	tests := []struct {
		name string
		path string
	}{
		{"no streamSettings", plain.write(t)},
		{"aliases", "testdata/aliases.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inbounds := readInbounds(t, tt.path)
			for _, in := range inbounds {
				if in.Err != nil {
					t.Fatalf("%s: %v", in.Tag, in.Err)
				}
			}
			drift, err := checkDrift(context.Background(), newUserService(runningFrom(t, tt.path)),
				slog.New(slog.DiscardHandler), inbounds)
			if err != nil {
				t.Fatal(err)
			}
			if len(drift) != 0 {
				t.Errorf("drift = %v, want none", drift)
			}
		})
	}
}

// TestCheckDriftDoesNotQuoteBuildErrors: Xray quotes a bad short id; the reason must not.
func TestCheckDriftDoesNotQuoteBuildErrors(t *testing.T) {
	const bad = "zzSHORTID-SECRET"
	base := newDriftConfig(t.TempDir())
	running := runningFrom(t, base.write(t))
	broken := base
	broken.shortID = bad
	inbounds := readInbounds(t, broken.write(t))
	if inbounds[0].Err != nil || inbounds[0].Tag != "reality" {
		t.Fatalf("want the reality inbound to pass validation, got %+v", inbounds[0].Err)
	}

	drift, err := checkDrift(context.Background(), newUserService(running),
		slog.New(slog.DiscardHandler), inbounds)
	if err != nil {
		t.Fatal(err)
	}
	reason := drift["reality"]
	if reason == nil || !strings.Contains(reason.Error(), "does not build") {
		t.Fatalf("reason = %v, want the fixed build text", reason)
	}
	if strings.Contains(reason.Error(), bad) {
		t.Errorf("reason quotes the short id: %v", reason)
	}
}

func TestCheckDriftUnusable(t *testing.T) {
	tests := []struct {
		name   string
		change func(c *driftConfig)
		want   bool
	}{
		{"protocol change", func(c *driftConfig) { c.realityProto = "vmess" }, true},
		{"port change", func(c *driftConfig) { c.realityPort = 8444 }, false},
		{"file does not build", func(c *driftConfig) { c.shortID = "zz" }, true},
		{"protocol change and file does not build", func(c *driftConfig) {
			c.realityProto = "vmess"
			c.shortID = "zz"
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := newDriftConfig(t.TempDir())
			running := runningFrom(t, base.write(t))
			tt.change(&base)
			inbounds := readInbounds(t, base.write(t))
			drift, err := checkDrift(context.Background(), newUserService(running),
				slog.New(slog.DiscardHandler), inbounds)
			if err != nil {
				t.Fatal(err)
			}
			var driftErr *driftError
			if !errors.As(drift["reality"], &driftErr) || driftErr.unusable != tt.want {
				t.Errorf("drift = %#v, want unusable = %v", drift["reality"], tt.want)
			}
		})
	}
}
