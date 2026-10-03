package pass

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"maps"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	awgv1 "github.com/mrcsin/awg-grpc/gen/awg/v1"

	"github.com/mrcsin/usher/internal/state"
)

type passEnv struct {
	t        *testing.T
	settings Settings
	client   *fakeClient
	logs     *bytes.Buffer
	pass     *Pass
	dial     Dial
	ifaces   []*awgv1.InterfaceStatus
}

func newPassEnv(t *testing.T, yml string, ifaces ...*awgv1.InterfaceStatus) *passEnv {
	t.Helper()
	root := t.TempDir()
	e := &passEnv{
		t: t,
		settings: Settings{
			Host:       netip.MustParseAddr("203.0.113.10"),
			DNS:        []netip.Addr{netip.MustParseAddr("1.1.1.1")},
			ConfigPath: filepath.Join(root, "usher.yml"),
			ClientsDir: filepath.Join(root, "clients"),
			StatePath:  filepath.Join(root, "users.json"),
		},
		logs:   &bytes.Buffer{},
		ifaces: ifaces,
	}
	e.client = &fakeClient{status: func() (*awgv1.GetStatusResponse, error) {
		return &awgv1.GetStatusResponse{Interfaces: e.ifaces}, nil
	}}
	if err := os.MkdirAll(e.settings.ClientsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	e.writeConfig(yml)
	e.dial = func() (awgv1.ManagementServiceClient, func(), error) { return e.client, func() {}, nil }
	e.newPass()
	return e
}

func (e *passEnv) newPass() {
	e.pass = New(e.settings, e.dial, slog.New(slog.NewTextHandler(e.logs, nil)))
}

func (e *passEnv) writeConfig(yml string) {
	e.t.Helper()
	if err := os.WriteFile(e.settings.ConfigPath, []byte(yml), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *passEnv) run() {
	e.pass.Run(context.Background())
}

func (e *passEnv) writeClient(rel, content string) {
	e.t.Helper()
	path := filepath.Join(e.settings.ClientsDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func (e *passEnv) clientFiles() map[string]string {
	e.t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(e.settings.ClientsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(e.settings.ClientsDir, path)
		content, err := os.ReadFile(path)
		files[filepath.ToSlash(rel)] = string(content)
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return files
}

func (e *passEnv) loadState() *state.State {
	e.t.Helper()
	st, err := state.Load(e.settings.StatePath)
	if err != nil {
		e.t.Fatal(err)
	}
	return st
}

func (e *passEnv) appliedInterfaces() []string {
	var names []string
	for _, r := range e.client.applied {
		names = append(names, r.GetInterfaceName())
	}
	return names
}

func named(name string, address string, params ...*awgv1.ConfigParam) *awgv1.InterfaceStatus {
	return &awgv1.InterfaceStatus{
		Name:         name,
		Present:      true,
		PublicKey:    bytes.Repeat([]byte{9}, 32),
		ListenPort:   51820,
		Addresses:    []string{address},
		ClientParams: params,
	}
}

func TestRunRequestShape(t *testing.T) {
	e := newPassEnv(t, "phone: [awg0]\nlaptop: [awg0]\nguest: []\n",
		named("awg0", "10.8.1.1/24"), named("awg1", "10.9.1.1/24"))

	e.run()

	if got := e.appliedInterfaces(); !slices.Equal(got, []string{"awg0", "awg1"}) {
		t.Fatalf("applied interfaces = %v", got)
	}
	first, second := e.client.applied[0], e.client.applied[1]
	if first.GetAllowEmpty() || len(first.GetPeers()) != 2 {
		t.Fatalf("awg0 request: allow_empty=%v peers=%d", first.GetAllowEmpty(), len(first.GetPeers()))
	}
	st := e.loadState()
	// peers are ordered by user name: laptop, phone
	for i, user := range []string{"laptop", "phone"} {
		p := first.GetPeers()[i]
		want := st.AWG["awg0/"+user]
		if !bytes.Equal(p.GetPublicKey(), want.PublicKey()) || len(p.GetPublicKey()) != 32 {
			t.Errorf("%s: public key mismatch", user)
		}
		if !bytes.Equal(p.GetPresharedKey(), want.PresharedKey[:]) {
			t.Errorf("%s: preshared key mismatch", user)
		}
		if p.GetAllowedIp() != want.Route().String() {
			t.Errorf("%s: allowed ip = %q", user, p.GetAllowedIp())
		}
	}
	if !second.GetAllowEmpty() || len(second.GetPeers()) != 0 {
		t.Errorf("awg1 request: allow_empty=%v peers=%d", second.GetAllowEmpty(), len(second.GetPeers()))
	}
	files := e.clientFiles()
	if len(files) != 2 || files["phone/awg0.conf"] == "" || files["laptop/awg0.conf"] == "" {
		t.Errorf("clients files = %v", slices.Sorted(maps.Keys(files)))
	}
}

func TestRunSavesStateBeforeApply(t *testing.T) {
	e := newPassEnv(t, "phone: [awg0]\n", named("awg0", "10.8.1.1/24"))
	var entriesAtApply int
	e.client.apply = func(*awgv1.ApplyPeersRequest) (*awgv1.ApplyPeersResponse, error) {
		entriesAtApply = len(e.loadState().AWG)
		return &awgv1.ApplyPeersResponse{}, nil
	}

	e.run()
	if entriesAtApply != 1 {
		t.Fatalf("state held %d entries at the first ApplyPeers, want 1", entriesAtApply)
	}

	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(e.settings.StatePath, old, old); err != nil {
		t.Fatal(err)
	}
	e.run()
	info, err := os.Stat(e.settings.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(old) {
		t.Errorf("state was saved on a pass that changed nothing")
	}
}

func TestRunInvalidConfig(t *testing.T) {
	t.Run("last valid file is used", func(t *testing.T) {
		e := newPassEnv(t, "phone: [awg0]\n", named("awg0", "10.8.1.1/24"))
		e.run()
		e.writeConfig("phone: [awg0\n")
		e.client.applied = nil

		e.run()

		if len(e.client.applied) != 1 || len(e.client.applied[0].GetPeers()) != 1 {
			t.Fatalf("expected the last valid file to be applied, got %d requests", len(e.client.applied))
		}
		if !strings.Contains(e.logs.String(), "usher.yml") {
			t.Errorf("log does not name the file: %s", e.logs)
		}
		if _, ok := e.clientFiles()["phone/awg0.conf"]; !ok {
			t.Errorf("phone config was removed")
		}
	})
	t.Run("no earlier valid file", func(t *testing.T) {
		e := newPassEnv(t, "phone: [awg0\n", named("awg0", "10.8.1.1/24"))
		e.writeClient("keep/awg0.conf", "keep")

		e.run()

		if e.client.statusCalls != 0 || len(e.client.applied) != 0 {
			t.Errorf("requests were sent: status=%d apply=%d", e.client.statusCalls, len(e.client.applied))
		}
		if _, ok := e.clientFiles()["keep/awg0.conf"]; !ok {
			t.Errorf("clients directory changed")
		}
		if !strings.Contains(e.logs.String(), "usher.yml") {
			t.Errorf("log does not name the file: %s", e.logs)
		}
	})
}

func TestRunStopsBeforeRequests(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(e *passEnv)
		wantStatus  int
		wantLogHas  string
		stateBefore string
	}{
		{"state error", func(e *passEnv) {
			if err := os.WriteFile(e.settings.StatePath, []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, 0, "loading state", "{"},
		{"get status error", func(e *passEnv) {
			e.client.status = func() (*awgv1.GetStatusResponse, error) { return nil, errors.New("down") }
		}, 1, "getting status", ""},
		{"state cannot be saved", func(e *passEnv) {
			e.settings.StatePath = filepath.Join(filepath.Dir(e.settings.StatePath), "missing", "users.json")
			e.newPass()
		}, 1, "saving state", ""},
		{"dial error", func(e *passEnv) {
			e.dial = func() (awgv1.ManagementServiceClient, func(), error) { return nil, nil, errors.New("no socket") }
			e.newPass()
		}, 0, "connecting to awg-grpc", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newPassEnv(t, "phone: [awg0]\n", named("awg0", "10.8.1.1/24"))
			e.writeClient("stale/awg0.conf", "stale")
			tt.setup(e)

			e.run()

			if len(e.client.applied) != 0 {
				t.Errorf("ApplyPeers was called")
			}
			if e.client.statusCalls != tt.wantStatus {
				t.Errorf("GetStatus calls = %d, want %d", e.client.statusCalls, tt.wantStatus)
			}
			if !strings.Contains(e.logs.String(), tt.wantLogHas) {
				t.Errorf("log lacks %q: %s", tt.wantLogHas, e.logs)
			}
			stateBytes, err := os.ReadFile(e.settings.StatePath)
			if err != nil && tt.stateBefore != "" {
				t.Errorf("state file: %v", err)
			}
			if string(stateBytes) != tt.stateBefore {
				t.Errorf("users.json = %q, want %q", stateBytes, tt.stateBefore)
			}
			files := e.clientFiles()
			if len(files) != 1 || files["stale/awg0.conf"] != "stale" {
				t.Errorf("clients changed: %v", files)
			}
		})
	}
}

func TestRunInterfaceFailure(t *testing.T) {
	missing := named("awg0", "10.8.1.1/24")
	missing.Present = false
	tests := []struct {
		name        string
		failing     *awgv1.InterfaceStatus
		applyFails  bool
		wantApplied []string
	}{
		{"not present", missing, false, []string{"awg1"}},
		{"no free address", named("awg0", "10.8.1.1/30"), false, []string{"awg1"}},
		{"render error", named("awg0", "10.8.1.1/24", &awgv1.ConfigParam{Key: "S1", Value: "a\nb"}), false, []string{"awg1"}},
		{"apply error", named("awg0", "10.8.1.1/24"), true, []string{"awg0", "awg1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newPassEnv(t, "phone: [awg0, awg1]\nlaptop: [awg0]\n", tt.failing, named("awg1", "10.9.1.1/24"))
			e.writeClient("phone/awg0.conf", "old phone")
			e.writeClient("laptop/awg0.conf", "old laptop")
			e.client.apply = func(r *awgv1.ApplyPeersRequest) (*awgv1.ApplyPeersResponse, error) {
				if tt.applyFails && r.GetInterfaceName() == "awg0" {
					return nil, errors.New("rejected")
				}
				return &awgv1.ApplyPeersResponse{}, nil
			}

			e.run()

			if got := e.appliedInterfaces(); !slices.Equal(got, tt.wantApplied) {
				t.Errorf("applied = %v, want %v", got, tt.wantApplied)
			}
			files := e.clientFiles()
			if files["phone/awg0.conf"] != "old phone" || files["laptop/awg0.conf"] != "old laptop" {
				t.Errorf("failed interface lost its files: %v", slices.Sorted(maps.Keys(files)))
			}
			if !strings.Contains(files["phone/awg1.conf"], "[Peer]") {
				t.Errorf("awg1 config missing")
			}
		})
	}
}

func TestRunUnknownInterface(t *testing.T) {
	e := newPassEnv(t, "phone: [awg0, awg9]\n", named("awg0", "10.8.1.1/24"))
	e.writeClient("phone/awg9.conf", "orphan")

	e.run()

	if got := e.appliedInterfaces(); !slices.Equal(got, []string{"awg0"}) {
		t.Errorf("applied = %v", got)
	}
	if !strings.Contains(e.logs.String(), "awg9") {
		t.Errorf("log does not name awg9: %s", e.logs)
	}
	files := e.clientFiles()
	if len(files) != 1 || files["phone/awg0.conf"] == "" {
		t.Errorf("clients files = %v", slices.Sorted(maps.Keys(files)))
	}
}

func TestRunSwitchOff(t *testing.T) {
	e := newPassEnv(t, "phone: [awg0]\nlaptop: [awg0]\n", named("awg0", "10.8.1.1/24"))
	e.run()
	laptop := e.loadState().AWG["awg0/laptop"]
	laptopPub := laptop.PublicKey()
	e.client.applied = nil
	e.client.apply = func(*awgv1.ApplyPeersRequest) (*awgv1.ApplyPeersResponse, error) {
		return &awgv1.ApplyPeersResponse{Removed: [][]byte{laptopPub}}, nil
	}
	e.logs.Reset()

	e.writeConfig("phone: [awg0]\nlaptop: []\n")
	e.run()

	if len(e.client.applied) != 1 || len(e.client.applied[0].GetPeers()) != 1 {
		t.Fatalf("expected one request with one peer")
	}
	if _, ok := e.clientFiles()["laptop/awg0.conf"]; ok {
		t.Errorf("laptop file stayed")
	}
	if got := e.loadState().AWG["awg0/laptop"]; got != laptop {
		t.Errorf("laptop entry changed: %+v", got)
	}
	if !strings.Contains(e.logs.String(), "removed=[laptop]") {
		t.Errorf("log does not name the removed user: %s", e.logs)
	}
}

func TestRunLogsResult(t *testing.T) {
	unknown := bytes.Repeat([]byte{7}, 32)
	tests := []struct {
		name     string
		response *awgv1.ApplyPeersResponse
		want     []string
		silent   bool
	}{
		{"user names", nil, []string{"added=[phone]"}, false},
		{"key without entry", &awgv1.ApplyPeersResponse{Updated: [][]byte{unknown}},
			[]string{base64.StdEncoding.EncodeToString(unknown)}, false},
		{"empty result", &awgv1.ApplyPeersResponse{}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newPassEnv(t, "phone: [awg0]\n", named("awg0", "10.8.1.1/24"))
			e.client.apply = func(r *awgv1.ApplyPeersRequest) (*awgv1.ApplyPeersResponse, error) {
				if tt.response != nil {
					return tt.response, nil
				}
				return &awgv1.ApplyPeersResponse{Added: [][]byte{r.GetPeers()[0].GetPublicKey()}}, nil
			}

			e.run()

			for _, w := range tt.want {
				if !strings.Contains(e.logs.String(), w) {
					t.Errorf("log lacks %q: %s", w, e.logs)
				}
			}
			if tt.silent && strings.Contains(e.logs.String(), "peers changed") {
				t.Errorf("empty result was logged: %s", e.logs)
			}
		})
	}
}

func TestRunLogHasNoSecrets(t *testing.T) {
	const headerKey = "HDRKEYSECRET"
	const leak = "LEAKSECRET"
	e := newPassEnv(t, "phone: [awg0, awg1]\n",
		named("awg0", "10.8.1.1/24", &awgv1.ConfigParam{Key: "HeaderProtectionKey", Value: headerKey}),
		named("awg1", "10.9.1.1/24", &awgv1.ConfigParam{Key: "S1", Value: "x\n" + leak}))
	e.client.apply = func(r *awgv1.ApplyPeersRequest) (*awgv1.ApplyPeersResponse, error) {
		return &awgv1.ApplyPeersResponse{Added: [][]byte{r.GetPeers()[0].GetPublicKey()}}, nil
	}

	e.run()
	e.writeConfig("phone: [awg0\n")
	e.run()

	logs := e.logs.String()
	if logs == "" {
		t.Fatal("no log output")
	}
	secrets := []string{headerKey, leak}
	for _, entry := range e.loadState().AWG {
		secrets = append(secrets, entry.PrivateKey.String(), entry.PresharedKey.String())
	}
	for _, s := range secrets {
		if strings.Contains(logs, s) {
			t.Errorf("log contains a secret: %q", s)
		}
	}
}
