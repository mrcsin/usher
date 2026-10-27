package xray

import (
	"bytes"
	"encoding/json"
	"flag"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/xtls/libxray/share"
	"github.com/xtls/xray-core/infra/conf"
	"google.golang.org/protobuf/proto"

	"github.com/mrcsin/usher/internal/clients"
)

var update = flag.Bool("update", false, "rewrite the golden files")

const goldenDir = "testdata/golden"

func TestGolden(t *testing.T) {
	inbounds := readInbounds(t, filepath.Join(goldenDir, "server.json"))
	var tags []string
	for _, in := range inbounds {
		tags = append(tags, in.Tag)
	}
	wantTags := []string{"vless-reality", "vless-xhttp", "vless-xhttp-extra", "vless-ws", "hy2", "hy2-hop",
		"hy2-pin", "vmess-ws", "trojan-raw"}
	slices.Sort(tags)
	slices.Sort(wantTags)
	if !slices.Equal(tags, wantTags) {
		t.Fatalf("golden server.json holds %v, want the shapes of the acceptance list %v", tags, wantTags)
	}
	for _, in := range inbounds {
		t.Run(in.Tag, func(t *testing.T) {
			if in.Err != nil {
				t.Fatal(in.Err)
			}
			files, err := renderFiles(slog.New(slog.DiscardHandler), in, "198.51.100.7", "alice", testEntry(in))
			if err != nil {
				t.Fatal(err)
			}
			checkGolden(t, files, in.Tag, JSONSuffix, true)
			checkGolden(t, files, in.Tag, LinkSuffix, in.Tag != "hy2-pin")

			clientJSON := files[clients.Path("alice", in.Tag, JSONSuffix)]
			var parsed conf.Config
			if err := json.Unmarshal(clientJSON, &parsed); err != nil {
				t.Fatalf("client config does not decode: %v", err)
			}
			for _, outbound := range parsed.OutboundConfigs {
				if _, err := outbound.Build(); err != nil {
					t.Errorf("outbound does not build: %v", err)
				}
			}
			if link, ok := files[clients.Path("alice", in.Tag, LinkSuffix)]; ok {
				back, err := share.ConvertShareLinksToXrayJson(string(link), "")
				if err != nil {
					t.Fatalf("link does not parse back: %v", err)
				}
				var round conf.Config
				if err := json.Unmarshal(back, &round); err != nil || len(round.OutboundConfigs) != 1 {
					t.Fatalf("link parsed back to %d outbounds, err %v, want 1", len(round.OutboundConfigs), err)
				}
				if in.Hints.HopPorts != "" {
					first, _, _ := strings.Cut(in.Hints.HopPorts, "-")
					parsed = withPort(t, clientJSON, first)
				}
				assertSameOutbound(t, parsed.OutboundConfigs[0], round.OutboundConfigs[0])
			}
		})
	}
}

func withPort(t *testing.T, clientJSON []byte, port string) conf.Config {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(clientJSON, &doc); err != nil {
		t.Fatal(err)
	}
	outbound := object(doc["outbounds"].([]any)[0])
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	object(outbound["settings"])["port"] = n
	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var cfg conf.Config
	if err := json.Unmarshal(encoded, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// assertSameOutbound requires the link and the JSON to build the same proxy and stream settings.
func assertSameOutbound(t *testing.T, fromJSON, fromLink conf.OutboundDetourConfig) {
	t.Helper()
	want, err := fromJSON.Build()
	if err != nil {
		t.Fatal(err)
	}
	got, err := fromLink.Build()
	if err != nil {
		t.Fatalf("outbound of the link does not build: %v", err)
	}
	wantProxy, _ := want.GetProxySettings().GetInstance()
	gotProxy, _ := got.GetProxySettings().GetInstance()
	if !proto.Equal(wantProxy, gotProxy) {
		t.Errorf("proxy settings differ:\n json %v\n link %v", wantProxy, gotProxy)
	}
	wantSender, _ := want.GetSenderSettings().GetInstance()
	gotSender, _ := got.GetSenderSettings().GetInstance()
	if !proto.Equal(wantSender, gotSender) {
		t.Errorf("sender settings differ:\n json %v\n link %v", wantSender, gotSender)
	}
}

func checkGolden(t *testing.T, files map[string][]byte, tag, suffix string, want bool) {
	t.Helper()
	got, present := files[clients.Path("alice", tag, suffix)]
	path := filepath.Join(goldenDir, tag+suffix)
	if !want {
		if present {
			t.Errorf("%s is present, want none", suffix)
		}
		return
	}
	if !present {
		t.Fatalf("no %s file", suffix)
	}
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	golden, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, golden) {
		t.Errorf("%s differs from %s, got:\n%s", suffix, path, got)
	}
}

func TestRenderJSONRejectsBadOutbound(t *testing.T) {
	tests := []struct {
		name     string
		outbound map[string]any
	}{
		{"unknown protocol", map[string]any{"protocol": "nope", "settings": map[string]any{}}},
		{"bad stream network", map[string]any{
			"protocol":       "trojan",
			"settings":       map[string]any{"address": "198.51.100.7", "port": 443, "password": "x"},
			"streamSettings": map[string]any{"network": "carrier-pigeon"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := renderJSON(tt.outbound); err == nil {
				t.Error("renderJSON succeeded")
			}
		})
	}
}

func TestRenderFilesLinkDefect(t *testing.T) {
	// libxray refuses a single-port hop while the outbound still builds: the .json stays and the
	// defect is logged without a secret.
	inbounds := readInbounds(t, filepath.Join(goldenDir, "server.json"))
	var hop inbound
	for _, in := range inbounds {
		if in.Tag == "hy2-hop" {
			hop = in
		}
	}
	hop.Hints.HopPorts = "20000-20000"
	logs := &bytes.Buffer{}
	files, err := renderFiles(slog.New(slog.NewTextHandler(logs, nil)), hop, "198.51.100.7", "alice", testEntry(hop))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := files[clients.Path("alice", "hy2-hop", JSONSuffix)]; !ok {
		t.Error("no .json after a link defect")
	}
	if _, ok := files[clients.Path("alice", "hy2-hop", LinkSuffix)]; ok {
		t.Error(".txt written although libxray refused the link")
	}
	if !strings.Contains(logs.String(), "defect") || strings.Contains(logs.String(), testPassword) {
		t.Errorf("log = %q, want the defect and no secret", logs.String())
	}
}
