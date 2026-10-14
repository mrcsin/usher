package xray

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudflare/circl/sign/mldsa/mldsa65"
	"github.com/google/uuid"
	"github.com/xtls/xray-core/infra/conf"

	"github.com/mrcsin/usher/internal/state"
)

const (
	testPrivateKey = "cEUEZw0y-gOyW9gPA93c1bt6TrCFqbsXu4o9dTC0Zt0"
	testPassword   = "user-password"
)

var testID = uuid.MustParse("3f2504e0-4f89-41d3-9a0c-0305e82c3301")

func testEntry(in Inbound) state.XrayEntry {
	if protocols[in.Protocol].usesID {
		return state.XrayEntry{ID: testID}
	}
	return state.XrayEntry{Password: testPassword}
}

func mirrorOne(t *testing.T, in Inbound) map[string]any {
	t.Helper()
	if in.Err != nil {
		t.Fatalf("inbound %q: %v", in.Tag, in.Err)
	}
	out, err := clientOutbound(in, "198.51.100.7", testEntry(in))
	if err != nil {
		t.Fatalf("clientOutbound(%q): %v", in.Tag, err)
	}
	return out
}

// wantJSON compares got with the JSON document want.
func wantJSON(t *testing.T, got map[string]any, want string) {
	t.Helper()
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var gotAny, wantAny any
	if err := json.Unmarshal(encoded, &gotAny); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &wantAny); err != nil {
		t.Fatalf("bad want: %v", err)
	}
	if !reflect.DeepEqual(gotAny, wantAny) {
		pretty, _ := json.MarshalIndent(gotAny, "", "  ")
		t.Errorf("outbound mismatch, got:\n%s", pretty)
	}
}

func byTag(t *testing.T, inbounds []Inbound) map[string]Inbound {
	t.Helper()
	m := map[string]Inbound{}
	for _, in := range inbounds {
		m[in.Tag] = in
	}
	return m
}

func TestClientOutboundShapes(t *testing.T) {
	inbounds, err := ReadConfig("testdata/server.json")
	if err != nil {
		t.Fatal(err)
	}
	tags := byTag(t, inbounds)

	tests := []struct {
		tag  string
		want string
	}{
		{"vless-reality", `{"tag":"vless-reality","protocol":"vless",
			"settings":{"address":"203.0.113.10","port":443,"id":"` + testID.String() + `",
				"encryption":"none","flow":"xtls-rprx-vision"},
			"streamSettings":{"network":"raw","security":"reality","realitySettings":{
				"serverName":"www.example.com","publicKey":"` + publicKeyOf(t, testPrivateKey) + `",
				"shortId":"ab12","fingerprint":"chrome","spiderX":"/"}}}`},
		{"xh", `{"tag":"xh","protocol":"vless",
			"settings":{"address":"198.51.100.7","port":2053,"id":"` + testID.String() + `",
				"encryption":"none","flow":""},
			"streamSettings":{"network":"xhttp","security":"tls",
				"tlsSettings":{"serverName":"xh.example.com","fingerprint":"chrome"},
				"xhttpSettings":{"path":"/xh","mode":"auto","extra":{"xPaddingBytes":"2000-3000"}}}}`},
		{"xh-extra", `{"tag":"xh-extra","protocol":"vless",
			"settings":{"address":"198.51.100.7","port":2054,"id":"` + testID.String() + `",
				"encryption":"none","flow":""},
			"streamSettings":{"network":"xhttp","security":"tls",
				"tlsSettings":{"serverName":"xh.example.com","fingerprint":"chrome"},
				"xhttpSettings":{"path":"/xh","extra":{"xPaddingBytes":"100-200"}}}}`},
		{"ws", `{"tag":"ws","protocol":"vless",
			"settings":{"address":"198.51.100.7","port":2083,"id":"` + testID.String() + `",
				"encryption":"none","flow":""},
			"streamSettings":{"network":"ws","security":"tls",
				"tlsSettings":{"serverName":"ws.example.com","fingerprint":"chrome"},
				"wsSettings":{"path":"/ws"}}}`},
		{"grpc", `{"tag":"grpc","protocol":"vless",
			"settings":{"address":"198.51.100.7","port":2087,"id":"` + testID.String() + `",
				"encryption":"none","flow":""},
			"streamSettings":{"network":"grpc","security":"tls",
				"tlsSettings":{"serverName":"g.example.com","fingerprint":"chrome"},
				"grpcSettings":{"serviceName":"svc"}}}`},
		{"hu", `{"tag":"hu","protocol":"vless",
			"settings":{"address":"198.51.100.7","port":2088,"id":"` + testID.String() + `",
				"encryption":"none","flow":""},
			"streamSettings":{"network":"httpupgrade","security":"tls",
				"tlsSettings":{"serverName":"hu.example.com","fingerprint":"chrome"},
				"httpupgradeSettings":{"path":"/hu"}}}`},
		{"hy2", `{"tag":"hy2","protocol":"hysteria",
			"settings":{"address":"198.51.100.7","port":8443,"version":2},
			"streamSettings":{"network":"hysteria","security":"tls",
				"tlsSettings":{"serverName":"hy.example.com","alpn":["h3"],"fingerprint":"chrome",
					"pinnedPeerCertSha256":"aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"},
				"hysteriaSettings":{"version":2,"auth":"` + testPassword + `"},
				"finalmask":{"udp":[
					{"type":"salamander","settings":{"password":"obfs"}},
					{"type":"udphop","settings":{"mode":"intervalLocal,intervalRemote",
						"interval":30,"remotePorts":"20000-30000"}}]}}}`},
		{"vmess-ws", `{"tag":"vmess-ws","protocol":"vmess",
			"settings":{"address":"198.51.100.7","port":2096,"id":"` + testID.String() + `","security":"auto"},
			"streamSettings":{"network":"ws","security":"none","wsSettings":{"path":"/vm"}}}`},
		{"trojan", `{"tag":"trojan","protocol":"trojan",
			"settings":{"address":"198.51.100.7","port":8444,"password":"` + testPassword + `"},
			"streamSettings":{"network":"raw","security":"tls",
				"tlsSettings":{"serverName":"t.example.com","fingerprint":"chrome"}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			in, ok := tags[tt.tag]
			if !ok {
				t.Fatalf("testdata has no inbound %q", tt.tag)
			}
			out := mirrorOne(t, in)
			wantJSON(t, out, tt.want)
			assertBuilds(t, out)
		})
	}
}

// assertBuilds checks that Xray accepts the outbound as a client config.
func assertBuilds(t *testing.T, outbound map[string]any) {
	t.Helper()
	encoded, err := json.Marshal(outbound)
	if err != nil {
		t.Fatal(err)
	}
	var detour conf.OutboundDetourConfig
	if err := json.Unmarshal(encoded, &detour); err != nil {
		t.Fatalf("outbound does not decode: %v", err)
	}
	if _, err := detour.Build(); err != nil {
		t.Fatalf("outbound does not build: %v", err)
	}
}

func publicKeyOf(t *testing.T, private string) string {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(private)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())
}

func TestClientOutboundVlessFlow(t *testing.T) {
	tests := []struct {
		name, kind, path string
		value            any
		want             string
	}{
		{"raw behind reality", "reality", "", nil, "xtls-rprx-vision"},
		{"raw behind tls", "vless-tls", "streamSettings.network", "raw", "xtls-rprx-vision"},
		{"raw without security", "vless-tls", "streamSettings.security", "none", ""},
		{"ws behind tls", "vless-tls", "", nil, ""},
		{"xhttp behind tls", "xhttp", "", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := baseInbound(t, tt.kind)
			if tt.kind == "reality" {
				setPath(raw, "streamSettings.realitySettings.privateKey", testPrivateKey)
			}
			if tt.path != "" {
				setPath(raw, tt.path, tt.value)
			}
			out := mirrorOne(t, readOne(t, raw))
			flow := object(out["settings"])["flow"]
			if flow != tt.want {
				t.Errorf("flow = %v, want %q", flow, tt.want)
			}
		})
	}
}

func TestClientOutboundTrust(t *testing.T) {
	cert, leafHash := selfSignedPEM(t)
	lines := make([]any, 0)
	for _, l := range strings.Split(strings.TrimSpace(cert), "\n") {
		lines = append(lines, l)
	}
	const explicit = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

	tests := []struct {
		name  string
		kind  string
		set   map[string]any
		want  string // expected pinnedPeerCertSha256, empty for none
		fails string
	}{
		{"ca by default", "vless-tls", nil, "", ""},
		{"ca ignores a certificate", "vless-tls", map[string]any{
			"streamSettings.tlsSettings.certificates": []any{map[string]any{"certificate": lines}}}, "", ""},
		{"pin from pin_sha256", "vless-tls", map[string]any{
			"x-usher.trust": "pin", "x-usher.pin_sha256": strings.ToUpper(explicit)}, explicit, ""},
		{"pin from inline lines", "vless-tls", map[string]any{
			"x-usher.trust": "pin",
			"streamSettings.tlsSettings.certificates": []any{map[string]any{"certificate": lines}}},
			leafHash, ""},
		{"pin on hysteria", "hysteria", map[string]any{
			"x-usher.trust": "pin", "x-usher.pin_sha256": explicit}, explicit, ""},
		{"pin with a bad inline certificate", "vless-tls", map[string]any{
			"x-usher.trust": "pin",
			"streamSettings.tlsSettings.certificates": []any{map[string]any{"certificate": []any{"not a pem"}}}},
			"", "x-usher.pin_sha256"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := baseInbound(t, tt.kind)
			for path, v := range tt.set {
				setPath(raw, path, v)
			}
			in := readOne(t, raw)
			if in.Err != nil {
				t.Fatal(in.Err)
			}
			out, err := clientOutbound(in, "h", testEntry(in))
			if tt.fails != "" {
				if err == nil || !strings.Contains(err.Error(), tt.fails) {
					t.Fatalf("err = %v, want it to name %q", err, tt.fails)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			tls := object(object(out["streamSettings"])["tlsSettings"])
			pin, present := tls["pinnedPeerCertSha256"]
			if tt.want == "" && present {
				t.Errorf("pin = %v, want none", pin)
			}
			if tt.want != "" && pin != tt.want {
				t.Errorf("pin = %v, want %s", pin, tt.want)
			}
			assertBuilds(t, out)
		})
	}
}

func selfSignedPEM(t *testing.T) (certPEM, hash string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "a.example.com"}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), hex.EncodeToString(sum[:])
}

func TestClientOutboundReality(t *testing.T) {
	var seed [mldsa65.SeedSize]byte
	for i := range seed {
		seed[i] = byte(i)
	}
	public, _ := mldsa65.NewKeyFromSeed(&seed)
	wantVerify := base64.RawURLEncoding.EncodeToString(public.Bytes())

	realityOf := func(out map[string]any) map[string]any {
		return object(object(out["streamSettings"])["realitySettings"])
	}
	base := func(t *testing.T) map[string]any {
		raw := baseInbound(t, "reality")
		setPath(raw, "streamSettings.realitySettings.privateKey", testPrivateKey)
		setPath(raw, "streamSettings.realitySettings.shortIds", []any{"ab12", "cd34"})
		return raw
	}

	t.Run("public key short id and spiderX", func(t *testing.T) {
		out := mirrorOne(t, readOne(t, base(t)))
		r := realityOf(out)
		if r["publicKey"] != publicKeyOf(t, testPrivateKey) || r["shortId"] != "ab12" || r["spiderX"] != "/" {
			t.Errorf("reality = %v", r)
		}
		if _, ok := r["mldsa65Verify"]; ok {
			t.Error("mldsa65Verify set without a seed")
		}
		assertBuilds(t, out)
	})
	t.Run("verify key from seed", func(t *testing.T) {
		raw := base(t)
		setPath(raw, "streamSettings.realitySettings.mldsa65Seed", base64.RawURLEncoding.EncodeToString(seed[:]))
		out := mirrorOne(t, readOne(t, raw))
		if got := realityOf(out)["mldsa65Verify"]; got != wantVerify {
			t.Errorf("mldsa65Verify = %v, want the key of the seed", got)
		}
		assertBuilds(t, out)
	})
	t.Run("no short ids", func(t *testing.T) {
		raw := base(t)
		setPath(raw, "streamSettings.realitySettings.shortIds", []any{})
		if got := realityOf(mirrorOne(t, readOne(t, raw)))["shortId"]; got != "" {
			t.Errorf("shortId = %v, want empty", got)
		}
	})
	t.Run("overrides", func(t *testing.T) {
		raw := base(t)
		setPath(raw, "x-usher.sni", "front.example.com")
		setPath(raw, "x-usher.fingerprint", "firefox")
		r := realityOf(mirrorOne(t, readOne(t, raw)))
		if r["serverName"] != "front.example.com" || r["fingerprint"] != "firefox" {
			t.Errorf("reality = %v", r)
		}
	})

	failures := []struct {
		name, field string
		value       any
	}{
		{"missing private key", "privateKey", nil},
		{"private key not base64", "privateKey", "!!!"},
		{"private key too short", "privateKey", "AAAA"},
		{"seed not base64", "mldsa65Seed", "!!!"},
		{"seed too short", "mldsa65Seed", "AAAA"},
	}
	for _, tt := range failures {
		t.Run(tt.name, func(t *testing.T) {
			raw := base(t)
			setPath(raw, "streamSettings.realitySettings."+tt.field, tt.value)
			in := readOne(t, raw)
			_, err := clientOutbound(in, "h", testEntry(in))
			if err == nil || !strings.Contains(err.Error(), tt.field) {
				t.Fatalf("err = %v, want it to name %s", err, tt.field)
			}
			if tt.value != nil && strings.Contains(err.Error(), tt.value.(string)) {
				t.Errorf("error quotes the value: %v", err)
			}
		})
	}
}

func TestClientOutboundXHTTP(t *testing.T) {
	tests := []struct {
		name  string
		xhttp map[string]any
		want  string
	}{
		{"plain", map[string]any{"path": "/x"}, `{"path":"/x"}`},
		{"other fields move into extra", map[string]any{
			"path": "/x", "host": "h.example.com", "mode": "packet-up",
			"sessionIDPlacement": "header", "xPaddingBytes": "10-20", "noSSEHeader": true},
			`{"path":"/x","host":"h.example.com","mode":"packet-up",
			"extra":{"sessionIDPlacement":"header","xPaddingBytes":"10-20","noSSEHeader":true}}`},
		{"server extra is verbatim and top-level fields are dropped", map[string]any{
			"path": "/x", "mode": "auto", "xPaddingBytes": "10-20",
			"extra": map[string]any{"xPaddingObfsMode": true, "xmux": map[string]any{"maxConcurrency": "16"}}},
			`{"path":"/x","mode":"auto",
			"extra":{"xPaddingObfsMode":true,"xmux":{"maxConcurrency":"16"}}}`},
		{"empty server extra falls back to the fields", map[string]any{
			"path": "/x", "xPaddingBytes": "10-20", "extra": map[string]any{}},
			`{"path":"/x","extra":{"xPaddingBytes":"10-20"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := baseInbound(t, "xhttp")
			setPath(raw, "streamSettings.xhttpSettings", tt.xhttp)
			out := mirrorOne(t, readOne(t, raw))
			got := object(object(out["streamSettings"])["xhttpSettings"])
			wantJSON(t, got, tt.want)
			assertBuilds(t, out)
		})
	}

	t.Run("result does not alias the config", func(t *testing.T) {
		raw := baseInbound(t, "xhttp")
		setPath(raw, "streamSettings.xhttpSettings.extra", map[string]any{"xmux": map[string]any{"a": "1"}})
		in := readOne(t, raw)
		out := mirrorOne(t, in)
		extra := object(object(object(out["streamSettings"])["xhttpSettings"])["extra"])
		object(extra["xmux"])["a"] = "changed"
		server := object(object(in.Raw["streamSettings"])["xhttpSettings"])
		if object(object(server["extra"])["xmux"])["a"] != "1" {
			t.Error("the result shares memory with Inbound.Raw")
		}
	})
}

func TestClientOutboundTransports(t *testing.T) {
	tests := []struct {
		name, key string
		server    map[string]any
		want      string
	}{
		{"ws path and host", "wsSettings",
			map[string]any{"path": "/w", "host": "w.example.com", "headers": map[string]any{"X": "y"},
				"acceptProxyProtocol": true},
			`{"path":"/w","host":"w.example.com"}`},
		{"httpupgrade path and host", "httpupgradeSettings",
			map[string]any{"path": "/h", "host": "h.example.com", "headers": map[string]any{"X": "y"}},
			`{"path":"/h","host":"h.example.com"}`},
		{"grpc service and authority", "grpcSettings",
			map[string]any{"serviceName": "svc", "authority": "a.example.com", "multiMode": true},
			`{"serviceName":"svc","authority":"a.example.com"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := baseInbound(t, "vless-tls")
			network := strings.TrimSuffix(tt.key, "Settings")
			setPath(raw, "streamSettings.network", network)
			setPath(raw, "streamSettings."+tt.key, tt.server)
			out := mirrorOne(t, readOne(t, raw))
			wantJSON(t, object(object(out["streamSettings"])[tt.key]), tt.want)
			assertBuilds(t, out)
		})
	}
}

func TestClientOutboundHysteria(t *testing.T) {
	t.Run("no hop keeps salamander only", func(t *testing.T) {
		raw := baseInbound(t, "hysteria")
		setPath(raw, "streamSettings.finalmask.quicParams", map[string]any{"congestion": "bbr"})
		setPath(raw, "streamSettings.finalmask.udp", []any{map[string]any{
			"type": "salamander", "settings": map[string]any{"password": "obfs"}}})
		out := mirrorOne(t, readOne(t, raw))
		stream := object(out["streamSettings"])
		wantJSON(t, object(stream["finalmask"]),
			`{"udp":[{"type":"salamander","settings":{"password":"obfs"}}]}`)
		wantJSON(t, object(stream["hysteriaSettings"]), `{"version":2,"auth":"`+testPassword+`"}`)
		assertBuilds(t, out)
	})
	t.Run("hop without salamander", func(t *testing.T) {
		raw := baseInbound(t, "hysteria")
		setPath(raw, "streamSettings.finalmask", nil)
		setPath(raw, "x-usher.hop_ports", "1000-2000")
		out := mirrorOne(t, readOne(t, raw))
		wantJSON(t, object(object(out["streamSettings"])["finalmask"]), `{"udp":[{"type":"udphop",
			"settings":{"mode":"intervalLocal,intervalRemote","interval":30,
			"remotePorts":"1000-2000"}}]}`)
		assertBuilds(t, out)
	})
	t.Run("neither mask nor hop", func(t *testing.T) {
		raw := baseInbound(t, "hysteria")
		setPath(raw, "streamSettings.finalmask", nil)
		out := mirrorOne(t, readOne(t, raw))
		if _, ok := object(out["streamSettings"])["finalmask"]; ok {
			t.Error("finalmask set")
		}
		assertBuilds(t, out)
	})
	t.Run("alpn is copied", func(t *testing.T) {
		raw := baseInbound(t, "hysteria")
		setPath(raw, "streamSettings.tlsSettings.alpn", []any{"h3"})
		tls := object(object(mirrorOne(t, readOne(t, raw))["streamSettings"])["tlsSettings"])
		if !reflect.DeepEqual(tls["alpn"], []any{"h3"}) {
			t.Errorf("alpn = %v", tls["alpn"])
		}
	})
}

func TestClientOutboundOverrides(t *testing.T) {
	raw := baseInbound(t, "vless-tls")
	setPath(raw, "x-usher", map[string]any{
		"address": "vpn.example.com", "port": 8443, "sni": "front.example.com", "fingerprint": "ios"})
	out := mirrorOne(t, readOne(t, raw))
	settings := object(out["settings"])
	if settings["address"] != "vpn.example.com" || settings["port"] != 8443 {
		t.Errorf("settings = %v", settings)
	}
	tls := object(object(out["streamSettings"])["tlsSettings"])
	if tls["serverName"] != "front.example.com" || tls["fingerprint"] != "ios" {
		t.Errorf("tlsSettings = %v", tls)
	}

	t.Run("host argument without an address hint", func(t *testing.T) {
		out := mirrorOne(t, readOne(t, baseInbound(t, "vless-tls")))
		if got := object(out["settings"])["address"]; got != "198.51.100.7" {
			t.Errorf("address = %v", got)
		}
	})
}

// TestClientOutboundLeaksNothing feeds an inbound full of server-only material and checks that
// none of it reaches any outbound.
func TestClientOutboundLeaksNothing(t *testing.T) {
	cert, _ := selfSignedPEM(t)
	var seed [mldsa65.SeedSize]byte
	seed[0] = 1
	mldsaSeed := base64.RawURLEncoding.EncodeToString(seed[:])
	const (
		keyPEM       = "-----BEGIN PRIVATE KEY-----\nMIGHAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBG0wawIBAQQg\n-----END PRIVATE KEY-----"
		certFile     = "/etc/xray/leaf.pem"
		keyFile      = "/etc/xray/leaf.key"
		fallbackDest = "fallback.internal:8080"
	)
	secrets := []string{testPrivateKey, mldsaSeed, certFile, keyFile, "MIGHAgEAMBMGByqGSM49AgEG",
		"sockopt", fallbackDest, "fallbacks", "privateKey", "mldsa65Seed", "certificateFile", "keyFile"}

	kinds := []string{"reality", "vless-tls", "xhttp", "hysteria"}
	for _, kind := range kinds {
		for _, trust := range []string{"ca", "pin"} {
			t.Run(kind+"/"+trust, func(t *testing.T) {
				raw := baseInbound(t, kind)
				setPath(raw, "settings.fallbacks", []any{map[string]any{"dest": fallbackDest}})
				setPath(raw, "streamSettings.sockopt", map[string]any{"mark": 255})
				setPath(raw, "streamSettings.tlsSettings.certificates", []any{map[string]any{
					"certificate":     strings.Split(cert, "\n"),
					"key":             strings.Split(keyPEM, "\n"),
					"certificateFile": certFile,
					"keyFile":         keyFile,
				}})
				setPath(raw, "streamSettings.realitySettings.privateKey", testPrivateKey)
				setPath(raw, "streamSettings.realitySettings.mldsa65Seed", mldsaSeed)
				setPath(raw, "streamSettings.finalmask.quicParams", map[string]any{"congestion": "bbr"})
				if trust == "pin" && kind != "reality" {
					setPath(raw, "x-usher.trust", "pin")
				}
				in := readOne(t, raw)
				out := mirrorOne(t, in)
				encoded, err := json.Marshal(out)
				if err != nil {
					t.Fatal(err)
				}
				for _, secret := range secrets {
					if strings.Contains(string(encoded), secret) {
						t.Errorf("outbound contains %q:\n%s", secret, encoded)
					}
				}
			})
		}
	}
}
