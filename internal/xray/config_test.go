package xray

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestReadConfigShapes(t *testing.T) {
	inbounds := readInbounds(t, "testdata/server.json")
	want := []inbound{
		{Tag: "vless-reality", Protocol: "vless", Port: 443, Network: "raw", Security: "reality",
			ServerName: "www.example.com",
			Hints:      hints{Address: "203.0.113.10", Port: 443, Fingerprint: "chrome", Trust: "ca"}},
		{Tag: "xh", Protocol: "vless", Port: 2053, Network: "xhttp", Security: "tls",
			ServerName: "xh.example.com", Hints: hints{Port: 2053, Fingerprint: "chrome", Trust: "ca"}},
		{Tag: "xh-extra", Protocol: "vless", Port: 2054, Network: "xhttp", Security: "tls",
			ServerName: "xh.example.com", Hints: hints{Port: 2054, Fingerprint: "chrome", Trust: "ca"}},
		{Tag: "ws", Protocol: "vless", Port: 2083, Network: "ws", Security: "tls",
			ServerName: "ws.example.com", Hints: hints{Port: 2083, Fingerprint: "chrome", Trust: "ca"}},
		{Tag: "grpc", Protocol: "vless", Port: 2087, Network: "grpc", Security: "tls",
			ServerName: "g.example.com", Hints: hints{Port: 2087, Fingerprint: "chrome", Trust: "ca"}},
		{Tag: "hu", Protocol: "vless", Port: 2088, Network: "httpupgrade", Security: "tls",
			ServerName: "hu.example.com", Hints: hints{Port: 2088, Fingerprint: "chrome", Trust: "ca"}},
		{Tag: "hy2", Protocol: "hysteria", Port: 8443, Network: "hysteria", Security: "tls",
			ServerName: "hy.example.com",
			Hints: hints{Port: 8443, Fingerprint: "chrome", Trust: "pin", HopPorts: "20000-30000",
				PinSHA256: "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"}},
		{Tag: "vmess-ws", Protocol: "vmess", Port: 2096, Network: "ws", Security: "none",
			Hints: hints{Port: 2096, Fingerprint: "chrome", Trust: "ca"}},
		{Tag: "trojan", Protocol: "trojan", Port: 8444, Network: "raw", Security: "tls",
			ServerName: "t.example.com", Hints: hints{Port: 8444, Fingerprint: "chrome", Trust: "ca"}},
	}
	if len(inbounds) != len(want) {
		t.Fatalf("got %d inbounds, want %d", len(inbounds), len(want))
	}
	for i, got := range inbounds {
		w := want[i]
		t.Run(w.Tag, func(t *testing.T) {
			if got.Err != nil {
				t.Fatalf("unexpected error: %v", got.Err)
			}
			if got.Tag != w.Tag || got.Protocol != w.Protocol || got.Port != w.Port ||
				got.Network != w.Network || got.Security != w.Security ||
				got.ServerName != w.ServerName || got.Hints != w.Hints {
				t.Errorf("got %+v, want %+v", summary(got), summary(w))
			}
		})
	}
}

func readInbounds(t *testing.T, path string) []inbound {
	t.Helper()
	cfg, err := readConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Inbounds
}

func summary(in inbound) string {
	return fmt.Sprintf("%s %s %d %s %s %q %+v", in.Tag, in.Protocol, in.Port, in.Network, in.Security,
		in.ServerName, in.Hints)
}

func TestReadConfigAliases(t *testing.T) {
	inbounds := readInbounds(t, "testdata/aliases.json")
	byTag := map[string]inbound{}
	for _, in := range inbounds {
		byTag[in.Tag] = in
	}
	tests := []struct {
		tag     string
		network string
		check   func(t *testing.T, in inbound)
	}{
		{"a1", "xhttp", func(t *testing.T, in inbound) { // method wins over network
			stream := object(in.Raw["streamSettings"])
			if _, ok := stream["method"]; ok {
				t.Error("method kept")
			}
			if object(stream["xhttpSettings"])["path"] != "/x" {
				t.Errorf("xhttpSettings = %v", stream["xhttpSettings"])
			}
			if in.Protocol != "vless" || in.Security != "tls" {
				t.Errorf("protocol/security = %s/%s", in.Protocol, in.Security)
			}
		}},
		{"a2", "ws", nil},
		{"a3", "raw", func(t *testing.T, in inbound) {
			if _, ok := object(in.Raw["streamSettings"])["rawSettings"]; !ok {
				t.Error("tcpSettings not moved to rawSettings")
			}
		}},
		{"a4", "hysteria", func(t *testing.T, in inbound) {
			settings := object(in.Raw["settings"])
			if _, ok := settings["users"]; ok {
				t.Error("users kept")
			}
			if _, ok := settings["clients"]; !ok {
				t.Error("users not moved to clients")
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			in, ok := byTag[tt.tag]
			if !ok {
				t.Fatal("inbound missing")
			}
			if in.Err != nil {
				t.Fatalf("unexpected error: %v", in.Err)
			}
			if in.Network != tt.network {
				t.Errorf("network = %q, want %q", in.Network, tt.network)
			}
			if tt.check != nil {
				tt.check(t, in)
			}
		})
	}
}

func baseInbound(t *testing.T, kind string) map[string]any {
	t.Helper()
	docs := map[string]string{
		"vless-tls": `{"tag":"t","protocol":"vless","port":443,"settings":{"clients":[],"decryption":"none"},
			"streamSettings":{"network":"ws","security":"tls","tlsSettings":{"serverName":"a.example.com"}}}`,
		"reality": `{"tag":"t","protocol":"vless","port":443,"settings":{"clients":[],"decryption":"none"},
			"streamSettings":{"network":"raw","security":"reality","realitySettings":{"serverNames":["a.example.com","b.example.com"],
				"privateKey":"` + testKey(1) + `","shortIds":["ab12"]}}}`,
		"xhttp": `{"tag":"t","protocol":"vless","port":443,"settings":{"clients":[],"decryption":"none"},
			"streamSettings":{"network":"xhttp","security":"tls","tlsSettings":{"serverName":"a.example.com"},
			"xhttpSettings":{"path":"/x"}}}`,
		"hysteria": `{"tag":"t","protocol":"hysteria","port":443,"settings":{"version":2,"clients":[]},
			"streamSettings":{"network":"hysteria","security":"tls","tlsSettings":{"serverName":"a.example.com"},
			"hysteriaSettings":{"version":2},
			"finalmask":{"udp":[{"type":"salamander","settings":{"password":"p"}}]}}}`,
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(docs[kind]), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func setPath(m map[string]any, path string, value any) {
	parts := strings.Split(path, ".")
	for _, p := range parts[:len(parts)-1] {
		next, ok := m[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[p] = next
		}
		m = next
	}
	last := parts[len(parts)-1]
	if value == nil {
		delete(m, last)
		return
	}
	m[last] = value
}

func readOne(t *testing.T, inbound map[string]any) inbound {
	t.Helper()
	data, err := json.Marshal(map[string]any{"inbounds": []any{inbound}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	inbounds := readInbounds(t, path)
	if len(inbounds) != 1 {
		t.Fatalf("got %d inbounds, want 1", len(inbounds))
	}
	return inbounds[0]
}

func TestReadConfigRejects(t *testing.T) {
	tests := []struct {
		name string
		kind string
		set  map[string]any
		want string
	}{
		{"port range", "vless-tls", map[string]any{"port": "443-445"}, "port: want a single integer"},
		{"port list", "vless-tls", map[string]any{"port": "443,444"}, "port:"},
		{"port zero", "vless-tls", map[string]any{"port": 0}, "port:"},
		{"port missing", "vless-tls", map[string]any{"port": nil}, "port:"},
		{"unix listen", "vless-tls", map[string]any{"listen": "/run/x.sock"}, "listen:"},
		{"abstract listen", "vless-tls", map[string]any{"listen": "@x"}, "listen:"},
		{"clients not empty", "vless-tls", map[string]any{"settings.clients": []any{map[string]any{"id": "x"}}}, "settings.clients"},
		{"decryption", "vless-tls", map[string]any{"settings.decryption": "mlkem"}, "settings.decryption"},
		{"decryption missing", "vless-tls", map[string]any{"settings.decryption": nil}, "settings.decryption"},
		{"kcp", "vless-tls", map[string]any{"streamSettings.network": "kcp"}, "streamSettings.network"},
		{"network not string", "vless-tls", map[string]any{"streamSettings.network": 3}, "streamSettings"},
		{"security", "vless-tls", map[string]any{"streamSettings.security": "xtls"}, "streamSettings.security"},
		{"raw http header", "vless-tls", map[string]any{
			"streamSettings.network": "raw", "streamSettings.rawSettings.header.type": "http"},
			"streamSettings.rawSettings.header.type"},
		{"alias clash", "vless-tls", map[string]any{
			"streamSettings.tcpSettings": map[string]any{}, "streamSettings.rawSettings": map[string]any{}},
			"tcpSettings"},
		{"tls without sni", "vless-tls", map[string]any{"streamSettings.tlsSettings.serverName": nil}, "x-usher.sni"},
		{"reality wildcard only", "reality", map[string]any{
			"streamSettings.realitySettings.serverNames": []any{"*.example.com", ""}},
			"realitySettings.serverNames"},
		{"reality sni outside serverNames", "reality", map[string]any{"x-usher.sni": "other.example.com"}, "x-usher.sni"},
		{"reality sni wildcard", "reality", map[string]any{
			"streamSettings.realitySettings.serverNames": []any{"*.example.com"},
			"x-usher.sni": "front.example.com"}, "x-usher.sni"},
		{"reality privateKey missing", "reality", map[string]any{"streamSettings.realitySettings.privateKey": nil},
			"realitySettings.privateKey"},
		{"reality privateKey std base64", "reality", map[string]any{
			"streamSettings.realitySettings.privateKey": "SECRETSECRET+/=="}, "realitySettings.privateKey"},
		{"reality mldsa65Seed format", "reality", map[string]any{
			"streamSettings.realitySettings.mldsa65Seed": "SECRETSEED"}, "realitySettings.mldsa65Seed"},
		{"reality seed equals privateKey", "reality", map[string]any{
			"streamSettings.realitySettings.mldsa65Seed": testKey(1)}, "realitySettings.mldsa65Seed"},
		{"reality fingerprint", "reality", map[string]any{"x-usher.fingerprint": "ios"}, "x-usher.fingerprint"},
		{"unknown x-usher key", "vless-tls", map[string]any{"x-usher.adress": "x"}, "x-usher"},
		{"x-usher port", "vless-tls", map[string]any{"x-usher.port": 70000}, "x-usher.port"},
		{"x-usher port type", "vless-tls", map[string]any{"x-usher.port": "443"}, "x-usher"},
		{"trust value", "vless-tls", map[string]any{"x-usher.trust": "none"}, "x-usher.trust"},
		{"pin without tls", "reality", map[string]any{"x-usher.trust": "pin"}, "x-usher.trust"},
		{"pin_sha256 without pin", "vless-tls", map[string]any{"x-usher.pin_sha256": strings.Repeat("a", 64)}, "x-usher.pin_sha256"},
		{"pin_sha256 format", "vless-tls", map[string]any{"x-usher.trust": "pin", "x-usher.pin_sha256": "abc"}, "x-usher.pin_sha256"},
		{"pin without source", "vless-tls", map[string]any{"x-usher.trust": "pin"}, "x-usher.pin_sha256"},
		{"pin with certificateFile only", "vless-tls", map[string]any{
			"x-usher.trust": "pin",
			"streamSettings.tlsSettings.certificates": []any{map[string]any{"certificateFile": "/c.pem"}}},
			"x-usher.pin_sha256"},
		{"hop on vless", "vless-tls", map[string]any{"x-usher.hop_ports": "20000-30000"}, "x-usher.hop_ports"},
		{"hop format", "hysteria", map[string]any{"x-usher.hop_ports": "30000-20000"}, "x-usher.hop_ports"},
		{"hop equal range", "hysteria", map[string]any{"x-usher.hop_ports": "20000-20000"}, "x-usher.hop_ports"},
		{"hop single port", "hysteria", map[string]any{"x-usher.hop_ports": "20000"}, "x-usher.hop_ports"},
		{"xhttp downloadSettings", "xhttp", map[string]any{
			"streamSettings.xhttpSettings.downloadSettings": map[string]any{}},
			"xhttpSettings.downloadSettings"},
		{"xhttp extra downloadSettings", "xhttp", map[string]any{
			"streamSettings.xhttpSettings.extra": map[string]any{"downloadSettings": map[string]any{}}},
			"xhttpSettings.extra.downloadSettings"},
		{"hysteria users and clients", "hysteria", map[string]any{
			"settings.users": []any{map[string]any{"auth": "x"}}, "settings.clients": []any{map[string]any{"auth": "y"}}},
			"settings.clients"},
		{"hysteria users", "hysteria", map[string]any{"settings.users": []any{map[string]any{"auth": "x"}}}, "settings.clients"},
		{"hysteria auth", "hysteria", map[string]any{"streamSettings.hysteriaSettings.auth": "secret"}, "hysteriaSettings.auth"},
		{"hysteria version", "hysteria", map[string]any{"streamSettings.hysteriaSettings.version": 1}, "hysteriaSettings.version"},
		{"hysteria settings version", "hysteria", map[string]any{"settings.version": nil}, "settings.version"},
		{"hysteria without tls", "hysteria", map[string]any{"streamSettings.security": "none"}, "streamSettings.security"},
		{"hysteria protocol on ws", "hysteria", map[string]any{"streamSettings.network": "ws"}, "streamSettings.network"},
		{"hysteria transport on vless", "vless-tls", map[string]any{"streamSettings.network": "hysteria"}, "streamSettings.network"},
		{"mask type", "hysteria", map[string]any{
			"streamSettings.finalmask.udp": []any{map[string]any{"type": "noise"}}},
			"finalmask.udp"},
		{"salamander packetSize", "hysteria", map[string]any{
			"streamSettings.finalmask.udp": []any{map[string]any{
				"type": "salamander", "settings": map[string]any{"password": "p", "packetSize": "100-200"}}}},
			"packetSize"},
		{"tcp mask", "hysteria", map[string]any{
			"streamSettings.finalmask.tcp": []any{map[string]any{"type": "fragment"}}},
			"finalmask.tcp"},
		{"salamander on vless", "vless-tls", map[string]any{
			"streamSettings.finalmask.udp": []any{map[string]any{"type": "salamander"}}},
			"finalmask.udp"},
		{"tag pattern", "vless-tls", map[string]any{"tag": "a/b"}, "tag:"},
		{"tag too long", "vless-tls", map[string]any{"tag": strings.Repeat("a", 33)}, "tag:"},
		{"sockopt type", "vless-tls", map[string]any{"streamSettings.sockopt": "x"}, "inbound:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := baseInbound(t, tt.kind)
			for path, v := range tt.set {
				setPath(m, path, v)
			}
			in := readOne(t, m)
			if in.Err == nil {
				t.Fatal("no error")
			}
			if !strings.Contains(in.Err.Error(), tt.want) {
				t.Errorf("error %q does not contain %q", in.Err, tt.want)
			}
		})
	}
}

func TestReadConfigAcceptsEdgeShapes(t *testing.T) {
	tests := []struct {
		name  string
		kind  string
		set   map[string]any
		check func(t *testing.T, in inbound)
	}{
		{"no streamSettings", "vless-tls", map[string]any{"streamSettings": nil}, func(t *testing.T, in inbound) {
			if in.Network != "raw" || in.Security != "none" || in.ServerName != "" {
				t.Errorf("got %s/%s/%q", in.Network, in.Security, in.ServerName)
			}
			if _, ok := in.Raw["streamSettings"]; ok {
				t.Error("streamSettings was added to the inbound")
			}
		}},
		{"sni override picks one of the serverNames", "reality", map[string]any{
			"x-usher.sni": "b.example.com"}, func(t *testing.T, in inbound) {
			if in.ServerName != "b.example.com" {
				t.Errorf("ServerName = %q", in.ServerName)
			}
		}},
		{"sni override beside wildcard entries", "reality", map[string]any{
			"streamSettings.realitySettings.serverNames": []any{"*.example.com", "front.example.com"},
			"x-usher.sni": "front.example.com"}, func(t *testing.T, in inbound) {
			if in.ServerName != "front.example.com" {
				t.Errorf("ServerName = %q", in.ServerName)
			}
		}},
		{"reality safari", "reality", map[string]any{"x-usher.fingerprint": "safari"}, func(t *testing.T, in inbound) {
			if in.Hints.Fingerprint != "safari" {
				t.Errorf("fingerprint = %q", in.Hints.Fingerprint)
			}
		}},
		{"pin from inline certificate", "vless-tls", map[string]any{
			"x-usher.trust": "pin",
			"streamSettings.tlsSettings.certificates": []any{map[string]any{"certificate": []any{"-----BEGIN CERTIFICATE-----"}}}},
			func(t *testing.T, in inbound) {
				if in.Hints.Trust != trustPin || in.Hints.PinSHA256 != "" {
					t.Errorf("hints = %+v", in.Hints)
				}
			}},
		{"x-usher port and sni", "vless-tls", map[string]any{"x-usher.port": 8443, "x-usher.sni": "o.example.com"},
			func(t *testing.T, in inbound) {
				if in.Hints.Port != 8443 || in.ServerName != "o.example.com" || in.Port != 443 {
					t.Errorf("port %d hints %+v sni %q", in.Port, in.Hints, in.ServerName)
				}
			}},
		{"hysteria quicParams", "hysteria", map[string]any{
			"streamSettings.finalmask.quicParams": map[string]any{"congestion": "bbr"}}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := baseInbound(t, tt.kind)
			for path, v := range tt.set {
				setPath(m, path, v)
			}
			in := readOne(t, m)
			if in.Err != nil {
				t.Fatalf("unexpected error: %v", in.Err)
			}
			if tt.check != nil {
				tt.check(t, in)
			}
		})
	}
}

func TestReadConfigErrorsDoNotQuoteValues(t *testing.T) {
	const secret = "s3cr3t-value-0123"
	for _, path := range []string{
		"streamSettings.hysteriaSettings.auth",
		"streamSettings.finalmask.udp",
	} {
		m := baseInbound(t, "hysteria")
		value := any(secret)
		if path == "streamSettings.finalmask.udp" {
			value = []any{map[string]any{"type": "salamander", "settings": map[string]any{"password": secret, "packetSize": secret}}}
		}
		setPath(m, path, value)
		in := readOne(t, m)
		if in.Err == nil {
			t.Fatalf("%s: no error", path)
		}
		if strings.Contains(in.Err.Error(), secret) {
			t.Errorf("%s: error quotes the value: %v", path, in.Err)
		}
	}
}

func TestReadConfigRealityErrorsDoNotQuoteKeys(t *testing.T) {
	const secret = "SECRETSECRET+/=="
	for _, field := range []string{"privateKey", "mldsa65Seed"} {
		m := baseInbound(t, "reality")
		setPath(m, "streamSettings.realitySettings."+field, secret)
		in := readOne(t, m)
		if in.Err == nil {
			t.Fatalf("%s: no error", field)
		}
		if strings.Contains(in.Err.Error(), secret) {
			t.Errorf("%s: error quotes the value: %v", field, in.Err)
		}
	}
	m := baseInbound(t, "reality")
	setPath(m, "streamSettings.realitySettings.mldsa65Seed", testKey(1))
	in := readOne(t, m)
	if in.Err == nil || strings.Contains(in.Err.Error(), testKey(1)) {
		t.Errorf("seed equal to privateKey: err = %v, want one without the key", in.Err)
	}
}

func TestReadConfigSkipsAndDuplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	doc := `{"inbounds":[
		{"tag":"x","protocol":"vless","port":1,"settings":{"clients":[],"decryption":"none"}},
		{"tag":"x","protocol":"trojan","port":2,"settings":{"clients":[]}},
		{"tag":"s","protocol":"shadowsocks","port":3,"settings":{}},
		{"protocol":"trojan","port":4,"settings":{"clients":[]}}
	]}`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	inbounds := readInbounds(t, path)
	if len(inbounds) != 2 {
		t.Fatalf("got %d inbounds, want 2", len(inbounds))
	}
	if inbounds[0].Err != nil {
		t.Errorf("first: %v", inbounds[0].Err)
	}
	if inbounds[1].Err == nil || !strings.Contains(inbounds[1].Err.Error(), "tag: defined twice") {
		t.Errorf("second: %v", inbounds[1].Err)
	}
}

func TestReadConfigFileErrors(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"inbounds": [`), 0o600); err != nil {
		t.Fatal(err)
	}
	notObject := filepath.Join(dir, "scalar.json")
	if err := os.WriteFile(notObject, []byte(`{"inbounds": [3]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(dir, "missing.json"), bad, notObject} {
		if _, err := readConfig(path); err == nil {
			t.Errorf("%s: no error", filepath.Base(path))
		}
	}
}

func TestDeployExampleConfig(t *testing.T) {
	path := filepath.Join("..", "..", "deploy", "xray", "config.json")
	wantTags := []string{"vless-reality", "hysteria2"}

	// As shipped, the placeholders make the Reality inbound fail on its key.
	inbounds := readInbounds(t, path)
	var tags []string
	for _, in := range inbounds {
		tags = append(tags, in.Tag)
	}
	if !slices.Equal(tags, wantTags) {
		t.Errorf("tags = %v, want %v", tags, wantTags)
	}
	if inbounds[0].Err == nil || !strings.Contains(inbounds[0].Err.Error(), "privateKey") {
		t.Errorf("shipped Reality inbound error = %v, want one about privateKey", inbounds[0].Err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	key := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	replaced := strings.ReplaceAll(string(data), "REPLACE_WITH_X25519_PRIVATE_KEY", key)
	filled := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(filled, []byte(replaced), 0o600); err != nil {
		t.Fatal(err)
	}
	inbounds = readInbounds(t, filled)
	for _, in := range inbounds {
		if in.Err != nil {
			t.Errorf("inbound %s: %v", in.Tag, in.Err)
		}
	}
}

func TestReadConfigStatsSwitches(t *testing.T) {
	const allOn = `{
		// stats switches, with comments
		"stats": {},
		"policy": {"levels": {"0": {"handshake": 4, "statsUserUplink": true, "statsUserDownlink": true,
			"statsUserOnline": true}}},
		"api": {"tag": "api", "services": ["HandlerService", "StatsService"]}, /* block comment */
		"inbounds": [{"tag":"t","protocol":"trojan","port":1,"settings":{"clients":[]}}]
	}`
	tests := []struct {
		name string
		doc  string
		want []string
	}{
		{"all present", allOn, nil},
		{"stats missing", strings.Replace(allOn, `"stats": {},`, "", 1), []string{"stats"}},
		{"stats null", strings.Replace(allOn, `"stats": {}`, `"stats": null`, 1), []string{"stats"}},
		{"uplink missing", strings.Replace(allOn, `"statsUserUplink": true,`, "", 1),
			[]string{"policy.levels.0.statsUserUplink"}},
		{"downlink false", strings.Replace(allOn, `"statsUserDownlink": true`, `"statsUserDownlink": false`, 1),
			[]string{"policy.levels.0.statsUserDownlink"}},
		{"online missing", strings.Replace(allOn, `,
			"statsUserOnline": true`, "", 1), []string{"policy.levels.0.statsUserOnline"}},
		{"other level only", strings.Replace(allOn, `"0":`, `"1":`, 1), []string{
			"policy.levels.0.statsUserUplink", "policy.levels.0.statsUserDownlink",
			"policy.levels.0.statsUserOnline"}},
		{"service missing", strings.Replace(allOn, `, "StatsService"`, "", 1),
			[]string{"StatsService in api.services"}},
		{"service in lower case", strings.Replace(allOn, `"StatsService"`, `"statsservice"`, 1), nil},
		{"online a string", strings.Replace(allOn, `"statsUserOnline": true`, `"statsUserOnline": "true"`, 1),
			[]string{"policy.levels.0.statsUserOnline"}},
		{"levels a list", strings.Replace(allOn, `"levels": {"0": {`, `"levels": [{"0": 0}], "x": {"0": {`, 1),
			[]string{"policy.levels.0.statsUserUplink", "policy.levels.0.statsUserDownlink",
				"policy.levels.0.statsUserOnline"}},
		{"services a string", strings.Replace(allOn, `["HandlerService", "StatsService"]`, `"StatsService"`, 1),
			[]string{"StatsService in api.services"}},
		{"none", `{"inbounds": []}`, []string{"stats", "policy.levels.0.statsUserUplink",
			"policy.levels.0.statsUserDownlink", "policy.levels.0.statsUserOnline",
			"StatsService in api.services"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(tt.doc), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := readConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(cfg.StatsMissing, tt.want) {
				t.Errorf("StatsMissing = %q, want %q", cfg.StatsMissing, tt.want)
			}
			if tt.name != "none" && (len(cfg.Inbounds) != 1 || cfg.Inbounds[0].Err != nil) {
				t.Errorf("inbounds = %+v", cfg.Inbounds)
			}
		})
	}
}
