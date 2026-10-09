package pass

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mrcsin/usher/internal/state"
)

// fakeSession is a Session that renders "new <interface>" into one file per user and enrolls a
// state entry the first time an interface has users.
type fakeSession struct {
	names  []string
	suffix string

	prepareErr map[string]error
	applyErr   map[string]error
	onApply    func(name string)

	applied map[string][]string
	closed  bool
}

func (f *fakeSession) Interfaces() []string { return f.names }

func (f *fakeSession) Prepare(st *state.State, name string, users []string) (map[string][]byte, bool, error) {
	if err := f.prepareErr[name]; err != nil {
		return nil, false, err
	}
	files := make(map[string][]byte, len(users))
	for _, user := range users {
		files[user+"/"+name+f.suffix] = []byte("new " + name)
	}
	entryName := state.EntryName("fake", name)
	_, enrolled := st.AWG[entryName]
	changed := len(users) > 0 && !enrolled
	if changed {
		st.AWG[entryName] = state.Entry{
			Address:      netip.MustParseAddr("10.0.0.2"),
			PrivateKey:   state.Key(bytes.Repeat([]byte{1}, state.KeyLength)),
			PresharedKey: state.Key(bytes.Repeat([]byte{2}, state.KeyLength)),
		}
	}
	return files, changed, nil
}

func (f *fakeSession) Apply(_ context.Context, _ *state.State, name string, users []string) error {
	if f.onApply != nil {
		f.onApply(name)
	}
	if f.applied == nil {
		f.applied = map[string][]string{}
	}
	f.applied[name] = users
	return f.applyErr[name]
}

func (f *fakeSession) Close() { f.closed = true }

type fakeBackend struct {
	session *fakeSession
	openErr error
	opens   int
}

func (f *fakeBackend) backend(name, suffix string) Backend {
	return Backend{Name: name, Suffix: suffix, Open: func(context.Context) (Session, error) {
		f.opens++
		if f.openErr != nil {
			return nil, f.openErr
		}
		f.session.suffix = suffix
		return f.session, nil
	}}
}

type passEnv struct {
	t        *testing.T
	settings Settings
	logs     *bytes.Buffer
	pass     *Pass
	backends []Backend
}

func newPassEnv(t *testing.T, yml string, backends ...Backend) *passEnv {
	t.Helper()
	root := t.TempDir()
	e := &passEnv{
		t: t,
		settings: Settings{
			ConfigPath: filepath.Join(root, "usher.yml"),
			ClientsDir: filepath.Join(root, "clients"),
			StatePath:  filepath.Join(root, "users.json"),
		},
		logs:     &bytes.Buffer{},
		backends: backends,
	}
	if err := os.MkdirAll(e.settings.ClientsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	e.writeConfig(yml)
	e.newPass()
	return e
}

func (e *passEnv) newPass() {
	e.pass = New(e.settings, e.backends, slog.New(slog.NewTextHandler(e.logs, nil)))
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

func sortedKeys(files map[string]string) []string {
	return slices.Sorted(maps.Keys(files))
}

func TestRunOneBackend(t *testing.T) {
	fb := &fakeBackend{session: &fakeSession{names: []string{"awg0", "awg1"}}}
	e := newPassEnv(t, "phone: [awg0]\nlaptop: [awg0]\nguest: []\n", fb.backend("awg-grpc", ".conf"))

	e.run()

	if got := fb.session.applied["awg0"]; !slices.Equal(got, []string{"laptop", "phone"}) {
		t.Errorf("awg0 users = %v", got)
	}
	if users, ok := fb.session.applied["awg1"]; !ok || len(users) != 0 {
		t.Errorf("awg1 applied = %v, %v", users, ok)
	}
	if got := sortedKeys(e.clientFiles()); !slices.Equal(got, []string{"laptop/awg0.conf", "phone/awg0.conf"}) {
		t.Errorf("client files = %v", got)
	}
	if !fb.session.closed {
		t.Error("session was not closed")
	}
}

func TestRunSavesStateBeforeApply(t *testing.T) {
	fb := &fakeBackend{session: &fakeSession{names: []string{"awg0"}}}
	e := newPassEnv(t, "phone: [awg0]\n", fb.backend("awg-grpc", ".conf"))
	var entriesAtApply int
	fb.session.onApply = func(string) {
		st, err := state.Load(e.settings.StatePath)
		if err != nil {
			t.Error(err)
			return
		}
		entriesAtApply = len(st.AWG)
	}

	e.run()
	if entriesAtApply != 1 {
		t.Fatalf("state held %d entries at the first Apply, want 1", entriesAtApply)
	}

	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(e.settings.StatePath, old, old); err != nil {
		t.Fatal(err)
	}
	e.writeConfig("phone: []\n")
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
		fb := &fakeBackend{session: &fakeSession{names: []string{"awg0"}}}
		e := newPassEnv(t, "phone: [awg0]\n", fb.backend("awg-grpc", ".conf"))
		e.run()
		e.writeConfig("phone: [awg0\n")
		fb.session.applied = nil

		e.run()

		if got := fb.session.applied["awg0"]; !slices.Equal(got, []string{"phone"}) {
			t.Fatalf("expected the last valid file to be applied, got %v", got)
		}
		if !strings.Contains(e.logs.String(), "usher.yml") {
			t.Errorf("log does not name the file: %s", e.logs)
		}
		if _, ok := e.clientFiles()["phone/awg0.conf"]; !ok {
			t.Errorf("phone config was removed")
		}
	})
	t.Run("no earlier valid file", func(t *testing.T) {
		fb := &fakeBackend{session: &fakeSession{names: []string{"awg0"}}}
		e := newPassEnv(t, "phone: [awg0\n", fb.backend("awg-grpc", ".conf"))
		e.writeClient("keep/awg0.conf", "keep")

		e.run()

		if fb.opens != 0 {
			t.Errorf("backend was opened")
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
		wantOpens   int
		wantLogHas  string
		stateBefore string
	}{
		{"state error", func(e *passEnv) {
			if err := os.WriteFile(e.settings.StatePath, []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, 0, "loading state", "{"},
		{"state cannot be saved", func(e *passEnv) {
			e.settings.StatePath = filepath.Join(filepath.Dir(e.settings.StatePath), "missing", "users.json")
			e.newPass()
		}, 1, "saving state", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fb := &fakeBackend{session: &fakeSession{names: []string{"awg0"}}}
			e := newPassEnv(t, "phone: [awg0]\n", fb.backend("awg-grpc", ".conf"))
			e.writeClient("stale/awg0.conf", "stale")
			tt.setup(e)

			e.run()

			if len(fb.session.applied) != 0 {
				t.Errorf("Apply was called")
			}
			if fb.opens != tt.wantOpens {
				t.Errorf("opens = %d, want %d", fb.opens, tt.wantOpens)
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
	tests := []struct {
		name        string
		session     *fakeSession
		wantApplied []string
	}{
		{"prepare error", &fakeSession{prepareErr: map[string]error{"awg0": errors.New("not present")}}, []string{"awg1"}},
		{"apply error", &fakeSession{applyErr: map[string]error{"awg0": errors.New("rejected")}}, []string{"awg0", "awg1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.session.names = []string{"awg0", "awg1"}
			fb := &fakeBackend{session: tt.session}
			e := newPassEnv(t, "phone: [awg0, awg1]\nlaptop: [awg0]\n", fb.backend("awg-grpc", ".conf"))
			e.writeClient("phone/awg0.conf", "old phone")
			e.writeClient("laptop/awg0.conf", "old laptop")

			e.run()

			if got := slices.Sorted(maps.Keys(tt.session.applied)); !slices.Equal(got, tt.wantApplied) {
				t.Errorf("applied = %v, want %v", got, tt.wantApplied)
			}
			files := e.clientFiles()
			if files["phone/awg0.conf"] != "old phone" || files["laptop/awg0.conf"] != "old laptop" {
				t.Errorf("failed interface lost its files: %v", sortedKeys(files))
			}
			if files["phone/awg1.conf"] != "new awg1" {
				t.Errorf("awg1 file missing: %v", sortedKeys(files))
			}
		})
	}
}

func TestRunUnknownInterface(t *testing.T) {
	fb := &fakeBackend{session: &fakeSession{names: []string{"awg0"}}}
	e := newPassEnv(t, "phone: [awg0, awg9]\n", fb.backend("awg-grpc", ".conf"))
	e.writeClient("phone/awg9.conf", "orphan")

	e.run()

	if !strings.Contains(e.logs.String(), "not reported by any backend") || !strings.Contains(e.logs.String(), "awg9") {
		t.Errorf("log does not name awg9: %s", e.logs)
	}
	files := e.clientFiles()
	if len(files) != 1 || files["phone/awg0.conf"] == "" {
		t.Errorf("clients files = %v", sortedKeys(files))
	}
}

func TestRunTwoBackends(t *testing.T) {
	awg := &fakeBackend{session: &fakeSession{names: []string{"awg0"}}}
	xray := &fakeBackend{session: &fakeSession{names: []string{"vless"}}}
	e := newPassEnv(t, "phone: [awg0, vless]\n", awg.backend("awg-grpc", ".conf"), xray.backend("xray", ".txt"))

	e.run()

	if got := awg.session.applied["awg0"]; !slices.Equal(got, []string{"phone"}) {
		t.Errorf("awg applied = %v", awg.session.applied)
	}
	if got := xray.session.applied["vless"]; !slices.Equal(got, []string{"phone"}) {
		t.Errorf("xray applied = %v", xray.session.applied)
	}
	want := []string{"phone/awg0.conf", "phone/vless.txt"}
	if got := sortedKeys(e.clientFiles()); !slices.Equal(got, want) {
		t.Errorf("client files = %v, want %v", got, want)
	}
	if strings.Contains(e.logs.String(), "not reported") {
		t.Errorf("unexpected unknown-name log: %s", e.logs)
	}
}

func TestRunNameCollision(t *testing.T) {
	awg := &fakeBackend{session: &fakeSession{names: []string{"shared", "awg0"}}}
	xray := &fakeBackend{session: &fakeSession{names: []string{"shared"}}}
	e := newPassEnv(t, "phone: [shared, awg0]\n", awg.backend("awg-grpc", ".conf"), xray.backend("xray", ".txt"))
	e.writeClient("phone/shared.conf", "old conf")
	e.writeClient("phone/shared.txt", "old txt")

	e.run()

	if _, ok := awg.session.applied["shared"]; ok {
		t.Error("awg applied the colliding name")
	}
	if _, ok := xray.session.applied["shared"]; ok {
		t.Error("xray applied the colliding name")
	}
	if got := awg.session.applied["awg0"]; !slices.Equal(got, []string{"phone"}) {
		t.Errorf("awg0 applied = %v", got)
	}
	if !strings.Contains(e.logs.String(), "awg-grpc and xray") {
		t.Errorf("log does not name both backends: %s", e.logs)
	}
	files := e.clientFiles()
	if files["phone/shared.conf"] != "old conf" || files["phone/shared.txt"] != "old txt" {
		t.Errorf("colliding interface lost its files: %v", sortedKeys(files))
	}
}

func TestRunFailedInterfaceKeepsOtherBackend(t *testing.T) {
	awg := &fakeBackend{session: &fakeSession{names: []string{"awg0"}, applyErr: map[string]error{"awg0": errors.New("rejected")}}}
	xray := &fakeBackend{session: &fakeSession{names: []string{"vless"}}}
	e := newPassEnv(t, "phone: [awg0, vless]\n", awg.backend("awg-grpc", ".conf"), xray.backend("xray", ".txt"))
	e.writeClient("phone/awg0.conf", "old conf")
	e.writeClient("phone/vless.txt", "old txt")

	e.run()

	if got := xray.session.applied["vless"]; !slices.Equal(got, []string{"phone"}) {
		t.Errorf("xray applied = %v", xray.session.applied)
	}
	files := e.clientFiles()
	if files["phone/awg0.conf"] != "old conf" {
		t.Errorf("failed awg interface lost its file: %v", files)
	}
	if files["phone/vless.txt"] != "new vless" {
		t.Errorf("xray file = %q", files["phone/vless.txt"])
	}
}

func TestRunBackendDown(t *testing.T) {
	awg := &fakeBackend{openErr: errors.New("no socket"), session: &fakeSession{}}
	xray := &fakeBackend{session: &fakeSession{names: []string{"vless"}}}
	e := newPassEnv(t, "phone: [awg0, vless]\n", awg.backend("awg-grpc", ".conf"), xray.backend("xray", ".txt"))
	e.writeClient("phone/awg0.conf", "old conf")
	e.writeClient("laptop/awg1.conf", "old laptop conf")
	e.writeClient("phone/vless.txt", "old txt")

	e.run()

	if got := xray.session.applied["vless"]; !slices.Equal(got, []string{"phone"}) {
		t.Errorf("xray applied = %v", xray.session.applied)
	}
	files := e.clientFiles()
	want := map[string]string{
		"phone/awg0.conf":  "old conf",
		"laptop/awg1.conf": "old laptop conf",
		"phone/vless.txt":  "new vless",
	}
	if !maps.Equal(files, want) {
		t.Errorf("client files = %v, want %v", files, want)
	}
	logs := e.logs.String()
	if !strings.Contains(logs, "backend is down") || !strings.Contains(logs, "no socket") {
		t.Errorf("log does not report the down backend: %s", logs)
	}
	if strings.Contains(logs, "not reported by any backend") {
		t.Errorf("unknown-name log while a backend is down: %s", logs)
	}
}

func TestRunAllBackendsDownKeepsFiles(t *testing.T) {
	fb := &fakeBackend{openErr: errors.New("no socket")}
	e := newPassEnv(t, "phone: [awg0]\n", fb.backend("awg-grpc", ".conf"))
	e.writeClient("phone/awg0.conf", "old")

	e.run()

	if files := e.clientFiles(); !maps.Equal(files, map[string]string{"phone/awg0.conf": "old"}) {
		t.Errorf("client files = %v", files)
	}
}

func TestRunLogsBackendName(t *testing.T) {
	fb := &fakeBackend{session: &fakeSession{names: []string{"awg0"}, prepareErr: map[string]error{"awg0": fmt.Errorf("boom")}}}
	e := newPassEnv(t, "phone: [awg0]\n", fb.backend("awg-grpc", ".conf"))

	e.run()

	if !strings.Contains(e.logs.String(), "backend=awg-grpc") {
		t.Errorf("log lacks the backend name: %s", e.logs)
	}
}
