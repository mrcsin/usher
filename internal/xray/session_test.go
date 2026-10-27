package xray

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/xtls/xray-core/app/proxyman/command"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/proxy/vmess"
	"google.golang.org/grpc"

	"github.com/mrcsin/usher/internal/clients"
	"github.com/mrcsin/usher/internal/state"
)

type userService struct {
	command.HandlerServiceClient
	running    []*core.InboundHandlerConfig
	users      map[string][]*protocol.User
	failAdd    map[string]bool
	failRemove map[string]bool
	listErr    error
	inboundErr error
	alters     int
}

func newUserService(running []*core.InboundHandlerConfig) *userService {
	return &userService{running: running, users: map[string][]*protocol.User{},
		failAdd: map[string]bool{}, failRemove: map[string]bool{}}
}

func (f *userService) ListInbounds(_ context.Context, _ *command.ListInboundsRequest,
	_ ...grpc.CallOption) (*command.ListInboundsResponse, error) {
	if f.inboundErr != nil {
		return nil, f.inboundErr
	}
	return &command.ListInboundsResponse{Inbounds: f.running}, nil
}

func (f *userService) GetInboundUsers(_ context.Context, req *command.GetInboundUserRequest,
	_ ...grpc.CallOption) (*command.GetInboundUserResponse, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return &command.GetInboundUserResponse{Users: f.users[req.GetTag()]}, nil
}

func (f *userService) AlterInbound(_ context.Context, req *command.AlterInboundRequest,
	_ ...grpc.CallOption) (*command.AlterInboundResponse, error) {
	f.alters++
	op, err := req.GetOperation().GetInstance()
	if err != nil {
		return nil, err
	}
	tag := req.GetTag()
	switch op := op.(type) {
	case *command.AddUserOperation:
		email := op.GetUser().GetEmail()
		if f.failAdd[email] {
			return nil, errors.New("add refused")
		}
		for _, u := range f.users[tag] {
			if u.GetEmail() == email {
				return nil, errors.New("user already exists")
			}
		}
		f.users[tag] = append(f.users[tag], op.GetUser())
	case *command.RemoveUserOperation:
		if f.failRemove[op.GetEmail()] {
			return nil, errors.New("remove refused")
		}
		before := len(f.users[tag])
		f.users[tag] = slices.DeleteFunc(f.users[tag], func(u *protocol.User) bool {
			return u.GetEmail() == op.GetEmail()
		})
		if len(f.users[tag]) == before {
			return nil, errors.New("user not found")
		}
	default:
		return nil, errors.New("unknown operation")
	}
	return &command.AlterInboundResponse{}, nil
}

func (f *userService) credentials(t *testing.T, in inbound) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, u := range f.users[in.Tag] {
		instance, err := u.GetAccount().GetInstance()
		if err != nil {
			t.Fatal(err)
		}
		out[u.GetEmail()] = protocols[in.Protocol].credential(instance)
	}
	return out
}

func (f *userService) seed(in inbound, email string, e state.XrayEntry) {
	f.users[in.Tag] = append(f.users[in.Tag], &protocol.User{Email: email, Account: serial.ToTypedMessage(protocols[in.Protocol].account(in, e))})
}

type sessionFixture struct {
	session *Session
	service *userService
	logs    *bytes.Buffer
	cfg     driftConfig
}

func newSessionFixture(t *testing.T, change func(c *driftConfig)) sessionFixture {
	t.Helper()
	cfg := newDriftConfig(t.TempDir())
	if change != nil {
		change(&cfg)
	}
	path := cfg.write(t)
	service := newUserService(runningFrom(t, path))
	logs := &bytes.Buffer{}
	log := slog.New(slog.NewTextHandler(logs, nil))
	dial := func() (Clients, func(), error) { return Clients{Handler: service}, func() {}, nil }
	session, err := Open(t.Context(), dial, filepath.Join(t.TempDir(), "xray.sock"), path, "198.51.100.7", log)
	if err != nil {
		t.Fatal(err)
	}
	return sessionFixture{session: session, service: service, logs: logs, cfg: cfg}
}

func (f sessionFixture) inbound(tag string) inbound {
	return f.session.byTag[tag]
}

func newState() *state.State {
	return &state.State{Version: 2, AWG: map[string]state.Entry{}, Xray: map[string]state.XrayEntry{}}
}

func idEntry() state.XrayEntry { return state.XrayEntry{ID: uuid.New()} }

// badInbound fails validation (clients are not empty) and still builds in Xray.
const badInbound = `{"tag": "bad", "protocol": "vless", "port": 9000,
	"settings": {"clients": [{"id": "5783a3e7-e373-51cd-8642-c83782b807c5"}], "decryption": "none"}},`

func TestOpenKeepsStatsMissing(t *testing.T) {
	f := newSessionFixture(t, nil)
	if !slices.Contains(f.session.statsMissing, "stats") ||
		!slices.Contains(f.session.statsMissing, "StatsService in api.services") {
		t.Errorf("statsMissing = %q, want the switches the drift config lacks", f.session.statsMissing)
	}
}

func TestSessionInterfaces(t *testing.T) {
	f := newSessionFixture(t, func(c *driftConfig) { c.extra = badInbound })
	if got, want := f.session.Interfaces(), []string{"bad", "reality", "ws", "hy2"}; !slices.Equal(got, want) {
		t.Errorf("Interfaces() = %v, want %v", got, want)
	}
}

func TestSessionInterfacesDeduplicates(t *testing.T) {
	dup := `{"tag": "ws", "protocol": "vless", "port": 9002,
		"settings": {"clients": [], "decryption": "none"}},`
	f := newSessionFixture(t, func(c *driftConfig) { c.extra = dup })
	if got, want := f.session.Interfaces(), []string{"ws", "reality", "hy2"}; !slices.Equal(got, want) {
		t.Errorf("Interfaces() = %v, want %v", got, want)
	}
	if err := f.session.byTag["ws"].Err; err == nil || !strings.Contains(err.Error(), "defined twice") {
		t.Errorf("ws entry error = %v, want the duplicate error", err)
	}
}

func TestSessionPrepare(t *testing.T) {
	f := newSessionFixture(t, nil)
	st := newState()
	seeded := idEntry()
	st.Xray["reality/bob"] = seeded

	files, changed, err := f.session.Prepare(st, "reality", []string{"alice", "bob"})
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for key := range files {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	wantKeys := []string{
		clients.Path("alice", "reality", JSONSuffix), clients.Path("alice", "reality", LinkSuffix),
		clients.Path("bob", "reality", JSONSuffix), clients.Path("bob", "reality", LinkSuffix),
	}
	if !changed || !slices.Equal(keys, wantKeys) {
		t.Errorf("changed = %v, files = %v, want changed and %v", changed, keys, wantKeys)
	}
	if st.Xray["reality/alice"].ID == uuid.Nil {
		t.Error("alice has no id")
	}
	if st.Xray["reality/bob"] != seeded {
		t.Error("the seeded entry of bob changed")
	}

	_, changed, err = f.session.Prepare(st, "reality", []string{"alice", "bob"})
	if err != nil || changed {
		t.Errorf("second Prepare = changed %v, err %v, want unchanged", changed, err)
	}
}

func TestSessionPrepareHysteriaPinHasNoLink(t *testing.T) {
	pinned := `{"tag": "hy2pin", "protocol": "hysteria", "port": 8444,
		"x-usher": {"trust": "pin", "pin_sha256": "` + strings.Repeat("ab", 32) + `"},
		"settings": {"version": 2, "clients": []},
		"streamSettings": {"network": "hysteria", "security": "tls",
			"tlsSettings": {"serverName": "hy.example.com", "certificates": [{"certificate": ["c"], "key": ["k"]}]},
			"hysteriaSettings": {"version": 2}}},`
	f := newSessionFixture(t, func(c *driftConfig) { c.extra = pinned })

	files, _, err := f.session.Prepare(newState(), "hy2pin", []string{"alice"})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[clients.Path("alice", "hy2pin", JSONSuffix)] == nil {
		t.Errorf("files = %v, want only alice/hy2pin.json", slices.Collect(maps.Keys(files)))
	}
}

// TestSessionBrokenInboundStillRevokes: Prepare keeps a reason for a hint, enroll or render error,
// and Apply still removes the users who left.
func TestSessionBrokenInboundStillRevokes(t *testing.T) {
	notPEM := `{"tag": "pinned", "protocol": "vless", "port": 9003,
		"x-usher": {"trust": "pin"},
		"settings": {"clients": [], "decryption": "none"},
		"streamSettings": {"network": "ws", "security": "tls",
			"tlsSettings": {"serverName": "p.example.com", "certificates": [{"certificate": ["x"], "key": ["y"]}]}}},`
	badHint := `{"tag": "hinted", "protocol": "vless", "port": 9004,
		"x-usher": {"trust": "sideways"},
		"settings": {"clients": [], "decryption": "none"}},`
	tests := []struct {
		name    string
		extra   string
		tag     string
		setup   func(st *state.State)
		wantErr string
	}{
		{"x-usher hint error", badHint, "hinted", func(*state.State) {}, "x-usher.trust"},
		{"credential of the wrong kind", "", "ws", func(st *state.State) {
			st.Xray["ws/alice"] = state.XrayEntry{Password: "wrong-kind"}
		}, "does not fit"},
		{"render error", notPEM, "pinned", func(*state.State) {}, "not a PEM certificate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newSessionFixture(t, func(c *driftConfig) { c.extra = tt.extra })
			st := newState()
			tt.setup(st)
			in := f.inbound(tt.tag)
			f.service.seed(in, tt.tag+"/alice", idEntry())
			f.service.seed(in, tt.tag+"/gone", idEntry())

			files, _, err := f.session.Prepare(st, tt.tag, []string{"alice"})
			if err != nil || files != nil {
				t.Fatalf("Prepare = %v, %v, want no files and no error", files, err)
			}
			f.service.alters = 0
			err = f.session.Apply(t.Context(), st, tt.tag, []string{"alice"})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Apply error = %v, want one with %q", err, tt.wantErr)
			}
			if got := f.service.credentials(t, in); len(got) != 1 || got[tt.tag+"/alice"] == "" {
				t.Errorf("users = %v, want only alice", got)
			}
			if f.service.alters != 1 {
				t.Errorf("AlterInbound calls = %d, want 1 removal", f.service.alters)
			}
			if !strings.Contains(f.logs.String(), "removed=["+tt.tag+"/gone]") {
				t.Errorf("log lacks the removal:\n%s", f.logs)
			}
		})
	}
}

// TestSessionApplyGuards: Apply changes no account unless the running inbound provably takes the
// file's accounts; a hint error must not skip the drift check.
func TestSessionApplyGuards(t *testing.T) {
	badTag := `{"tag": "bad tag", "protocol": "vless", "port": 9005,
		"settings": {"clients": [], "decryption": "none"}},`
	dup := `{"tag": "ws", "protocol": "vless", "port": 9002,
		"settings": {"clients": [{"id": "5783a3e7-e373-51cd-8642-c83782b807c5"}], "decryption": "none"}},`
	badHint := `"x-usher": {"adress": "x"},`
	tests := []struct {
		name           string
		change         func(c *driftConfig)
		tag            string
		prepareFails   bool
		wantApplyError string
	}{
		{"static clients", func(c *driftConfig) { c.extra = badInbound }, "bad", true, ""},
		{"invalid tag", func(c *driftConfig) { c.extra = badTag }, "bad tag", true, ""},
		{"duplicate tag", func(c *driftConfig) { c.extra = dup }, "ws", true, ""},
		{"proxy type differs and the file does not build", func(c *driftConfig) {
			c.realityProto, c.shortID = "trojan", "zz"
		}, "reality", false, ""},
		{"hint error, proxy type differs", func(c *driftConfig) {
			c.realityProto, c.realityExtra = "trojan", badHint
		}, "reality", false, "proxy type"},
		{"hint error, file does not build", func(c *driftConfig) {
			c.shortID, c.realityExtra = "zz", badHint
		}, "reality", false, "comparing with"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := newDriftConfig(t.TempDir())
			running := runningFrom(t, base.write(t))
			file := base
			tt.change(&file)

			service := newUserService(running)
			dial := func() (Clients, func(), error) { return Clients{Handler: service}, func() {}, nil }
			session, err := Open(t.Context(), dial, filepath.Join(t.TempDir(), "xray.sock"), file.write(t),
				"198.51.100.7", slog.New(slog.DiscardHandler))
			if err != nil {
				t.Fatal(err)
			}
			service.users[tt.tag] = []*protocol.User{{Email: "operator-own"}}
			service.alters = 0

			st := newState()
			files, changed, err := session.Prepare(st, tt.tag, []string{"alice"})
			if tt.prepareFails {
				if err == nil || files != nil || changed {
					t.Fatalf("Prepare = %v, %v, %v, want the validation error", files, changed, err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			err = session.Apply(t.Context(), st, tt.tag, []string{"alice"})
			if err == nil || !strings.Contains(err.Error(), tt.wantApplyError) {
				t.Errorf("Apply error = %v, want one with %q", err, tt.wantApplyError)
			}
			if service.alters != 0 {
				t.Errorf("AlterInbound calls = %d, want 0", service.alters)
			}
		})
	}
}

func TestSessionPrepareKeepsChangedWhenRenderFails(t *testing.T) {
	notPEM := `{"tag": "pinned", "protocol": "vless", "port": 9003,
		"x-usher": {"trust": "pin"},
		"settings": {"clients": [], "decryption": "none"},
		"streamSettings": {"network": "ws", "security": "tls",
			"tlsSettings": {"serverName": "p.example.com", "certificates": [{"certificate": ["x"], "key": ["y"]}]}}},`
	f := newSessionFixture(t, func(c *driftConfig) { c.extra = notPEM })
	st := newState()
	_, changed, err := f.session.Prepare(st, "pinned", []string{"alice"})
	if err != nil || !changed || st.Xray["pinned/alice"].ID == uuid.Nil {
		t.Errorf("changed = %v, err = %v, entry = %v, want the enrolled entry reported", changed, err, st.Xray["pinned/alice"])
	}
}

func TestSessionApply(t *testing.T) {
	tests := []struct {
		name  string
		tag   string
		setup func(f sessionFixture, st *state.State)
		users []string
		want  func(st *state.State) map[string]string
		alter int
		logs  []string
	}{
		{
			name:  "adds missing users",
			tag:   "reality",
			users: []string{"alice", "bob"},
			setup: func(_ sessionFixture, st *state.State) {
				st.Xray["reality/alice"], st.Xray["reality/bob"] = idEntry(), idEntry()
			},
			want: func(st *state.State) map[string]string {
				return map[string]string{
					"reality/alice": st.Xray["reality/alice"].ID.String() + " xtls-rprx-vision",
					"reality/bob":   st.Xray["reality/bob"].ID.String() + " xtls-rprx-vision",
				}
			},
			alter: 2,
			logs:  []string{"users changed", `added="[reality/alice reality/bob]"`, "removed=[]", "updated=[]"},
		},
		{
			name:  "no change makes no call",
			tag:   "ws",
			users: []string{"alice"},
			setup: func(f sessionFixture, st *state.State) {
				st.Xray["ws/alice"] = idEntry()
				f.service.seed(f.inbound("ws"), "ws/alice", st.Xray["ws/alice"])
			},
			want: func(st *state.State) map[string]string {
				return map[string]string{"ws/alice": st.Xray["ws/alice"].ID.String() + " "}
			},
		},
		{
			name:  "removes a user who left and a foreign email",
			tag:   "hy2",
			users: []string{"alice"},
			setup: func(f sessionFixture, st *state.State) {
				st.Xray["hy2/alice"] = state.XrayEntry{Password: "alice-pass"}
				in := f.inbound("hy2")
				f.service.seed(in, "hy2/alice", st.Xray["hy2/alice"])
				f.service.seed(in, "hy2/carol", state.XrayEntry{Password: "carol-pass"})
				f.service.seed(in, "stranger", state.XrayEntry{Password: "other-pass"})
			},
			want:  func(*state.State) map[string]string { return map[string]string{"hy2/alice": "alice-pass"} },
			alter: 2,
			logs:  []string{`removed="[hy2/carol stranger]"`, "added=[]", "updated=[]"},
		},
		{
			name:  "replaces a changed credential",
			tag:   "hy2",
			users: []string{"alice"},
			setup: func(f sessionFixture, st *state.State) {
				st.Xray["hy2/alice"] = state.XrayEntry{Password: "new-pass"}
				f.service.seed(f.inbound("hy2"), "hy2/alice", state.XrayEntry{Password: "old-pass"})
			},
			want:  func(*state.State) map[string]string { return map[string]string{"hy2/alice": "new-pass"} },
			alter: 2,
			logs:  []string{"updated=[hy2/alice]", "added=[]", "removed=[]"},
		},
		{
			name:  "no users empties the inbound",
			tag:   "hy2",
			users: nil,
			setup: func(f sessionFixture, _ *state.State) {
				f.service.seed(f.inbound("hy2"), "hy2/alice", state.XrayEntry{Password: "alice-pass"})
			},
			want:  func(*state.State) map[string]string { return map[string]string{} },
			alter: 1,
			logs:  []string{"removed=[hy2/alice]"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newSessionFixture(t, nil)
			st := newState()
			tt.setup(f, st)
			f.service.alters = 0
			if err := f.session.Apply(t.Context(), st, tt.tag, tt.users); err != nil {
				t.Fatal(err)
			}
			got := f.service.credentials(t, f.inbound(tt.tag))
			if want := tt.want(st); !maps.Equal(got, want) {
				t.Errorf("users = %v, want %v", got, want)
			}
			if f.service.alters != tt.alter {
				t.Errorf("AlterInbound calls = %d, want %d", f.service.alters, tt.alter)
			}
			for _, s := range tt.logs {
				if !strings.Contains(f.logs.String(), s) {
					t.Errorf("log lacks %q:\n%s", s, f.logs)
				}
			}
			if strings.Contains(f.logs.String(), "-pass") {
				t.Errorf("log holds a password:\n%s", f.logs)
			}
			for _, e := range st.Xray {
				if e.ID != uuid.Nil && strings.Contains(f.logs.String(), e.ID.String()) {
					t.Errorf("log holds an id:\n%s", f.logs)
				}
			}
		})
	}
}

func TestSessionApplyUnderDrift(t *testing.T) {
	f := newSessionFixture(t, nil)
	f.cfg.realityPort = 8444
	path := f.cfg.write(t)
	dial := func() (Clients, func(), error) { return Clients{Handler: f.service}, func() {}, nil }
	session, err := Open(t.Context(), dial, filepath.Join(t.TempDir(), "xray.sock"), path, "198.51.100.7", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	in := session.byTag["reality"]
	f.service.seed(in, "reality/gone", idEntry())
	f.service.seed(in, "reality/alice", idEntry())
	st := newState()
	st.Xray["reality/alice"] = idEntry()

	err = session.Apply(t.Context(), st, "reality", []string{"alice"})
	if err == nil || !strings.Contains(err.Error(), "port") {
		t.Fatalf("Apply error = %v, want a drift error naming the port", err)
	}
	want := map[string]string{"reality/alice": st.Xray["reality/alice"].ID.String() + " xtls-rprx-vision"}
	if got := f.service.credentials(t, in); !maps.Equal(got, want) {
		t.Errorf("users = %v, want %v", got, want)
	}
}

func openWithConfig(t *testing.T, service *userService, cfg driftConfig) *Session {
	t.Helper()
	dial := func() (Clients, func(), error) { return Clients{Handler: service}, func() {}, nil }
	session, err := Open(t.Context(), dial, filepath.Join(t.TempDir(), "xray.sock"), cfg.write(t),
		"198.51.100.7", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func TestSessionApplyUnusableDriftChangesNothing(t *testing.T) {
	tests := []struct {
		name    string
		change  func(c *driftConfig)
		running func(running []*core.InboundHandlerConfig) []*core.InboundHandlerConfig
		want    string
	}{
		{"protocol change", func(c *driftConfig) { c.realityProto = "trojan" }, nil, "proxy type"},
		{"tag missing at runtime", nil, func(running []*core.InboundHandlerConfig) []*core.InboundHandlerConfig {
			return slices.DeleteFunc(running, func(r *core.InboundHandlerConfig) bool { return r.Tag == "reality" })
		}, "not running"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newSessionFixture(t, nil)
			cfg := f.cfg
			if tt.change != nil {
				tt.change(&cfg)
			}
			if tt.running != nil {
				f.service.running = tt.running(f.service.running)
			}
			session := openWithConfig(t, f.service, cfg)
			f.service.seed(f.inbound("reality"), "reality/gone", idEntry())
			st := newState()
			st.Xray["reality/alice"] = idEntry()
			f.service.alters = 0

			err := session.Apply(t.Context(), st, "reality", []string{"alice"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Apply error = %v, want one with %q", err, tt.want)
			}
			if f.service.alters != 0 {
				t.Errorf("AlterInbound calls = %d, want 0: an account of the wrong type crashes Xray", f.service.alters)
			}
		})
	}
}

func TestSessionApplyReportsReconcileAndDriftErrors(t *testing.T) {
	f := newSessionFixture(t, nil)
	f.cfg.realityPort = 8444
	session := openWithConfig(t, f.service, f.cfg)
	f.service.listErr = errors.New("api down")

	err := session.Apply(t.Context(), newState(), "reality", []string{"alice"})
	if err == nil || !strings.Contains(err.Error(), "api down") || !strings.Contains(err.Error(), "port") {
		t.Errorf("Apply error = %v, want the list error and the drift reason", err)
	}
}

func TestSessionApplyFailures(t *testing.T) {
	t.Run("removal fails, the add is skipped", func(t *testing.T) {
		f := newSessionFixture(t, nil)
		st := newState()
		st.Xray["hy2/alice"] = state.XrayEntry{Password: "new-pass"}
		f.service.seed(f.inbound("hy2"), "hy2/alice", state.XrayEntry{Password: "old-pass"})
		f.service.failRemove["hy2/alice"] = true

		err := f.session.Apply(t.Context(), st, "hy2", []string{"alice"})
		if err == nil || !strings.Contains(err.Error(), "removing hy2/alice") {
			t.Fatalf("Apply error = %v, want the removal error", err)
		}
		if got := f.service.credentials(t, f.inbound("hy2")); got["hy2/alice"] != "old-pass" {
			t.Errorf("users = %v, want the old account untouched", got)
		}
		if f.service.alters != 1 {
			t.Errorf("AlterInbound calls = %d, want only the failed removal", f.service.alters)
		}
	})
	t.Run("listing fails", func(t *testing.T) {
		f := newSessionFixture(t, nil)
		f.service.listErr = errors.New("api down")
		err := f.session.Apply(t.Context(), newState(), "hy2", []string{"alice"})
		if err == nil || !strings.Contains(err.Error(), "listing users") {
			t.Fatalf("Apply error = %v, want the listing error", err)
		}
		if f.service.alters != 0 {
			t.Errorf("AlterInbound calls = %d, want 0", f.service.alters)
		}
	})
	t.Run("changed credential, the new add fails", func(t *testing.T) {
		f := newSessionFixture(t, nil)
		st := newState()
		st.Xray["hy2/alice"] = state.XrayEntry{Password: "new-pass"}
		f.service.seed(f.inbound("hy2"), "hy2/alice", state.XrayEntry{Password: "old-pass"})
		f.service.failAdd["hy2/alice"] = true

		err := f.session.Apply(t.Context(), st, "hy2", []string{"alice"})
		if err == nil || !strings.Contains(err.Error(), "adding hy2/alice") {
			t.Fatalf("Apply error = %v, want the add error", err)
		}
		if got := f.service.credentials(t, f.inbound("hy2")); len(got) != 0 {
			t.Errorf("users = %v, want none: the old account is gone", got)
		}
		if !strings.Contains(f.logs.String(), "removed=[hy2/alice]") || !strings.Contains(f.logs.String(), "updated=[]") {
			t.Errorf("log should report the user as removed, not updated:\n%s", f.logs)
		}
	})
}

func TestOpenFailures(t *testing.T) {
	driftFails := newUserService(nil)
	driftFails.inboundErr = errors.New("api down")
	tests := []struct {
		name       string
		service    *userService // nil makes the dial fail
		wantErr    string
		wantClosed bool
	}{
		{"drift check fails", driftFails, "api down", true},
		{"dial fails", nil, "connecting to xray", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			closed := false
			dial := func() (Clients, func(), error) {
				if tt.service == nil {
					return Clients{}, nil, errors.New("no socket")
				}
				return Clients{Handler: tt.service}, func() { closed = true }, nil
			}
			cfg := newDriftConfig(t.TempDir())
			_, err := Open(t.Context(), dial, filepath.Join(t.TempDir(), "xray.sock"), cfg.write(t),
				"198.51.100.7", slog.New(slog.DiscardHandler))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Open error = %v, want one with %q", err, tt.wantErr)
			}
			if closed != tt.wantClosed {
				t.Errorf("connection closed = %v, want %v", closed, tt.wantClosed)
			}
		})
	}
}

func TestSessionApplyPartialFailureConverges(t *testing.T) {
	f := newSessionFixture(t, nil)
	st := newState()
	st.Xray["ws/alice"], st.Xray["ws/bob"] = idEntry(), idEntry()
	f.service.failAdd["ws/alice"] = true

	err := f.session.Apply(t.Context(), st, "ws", []string{"alice", "bob"})
	if err == nil || !strings.Contains(err.Error(), "ws/alice") {
		t.Fatalf("Apply error = %v, want one naming ws/alice", err)
	}
	if got := f.service.credentials(t, f.inbound("ws")); len(got) != 1 || got["ws/bob"] == "" {
		t.Fatalf("after the failure users = %v, want only ws/bob", got)
	}

	delete(f.service.failAdd, "ws/alice")
	if err := f.session.Apply(t.Context(), st, "ws", []string{"alice", "bob"}); err != nil {
		t.Fatal(err)
	}
	if got := f.service.credentials(t, f.inbound("ws")); len(got) != 2 {
		t.Errorf("after the retry users = %v, want both", got)
	}
}

func TestVMessEchoIsNoChange(t *testing.T) {
	in := inbound{Tag: "vm", Protocol: "vmess"}
	e := idEntry()
	service := newUserService(nil)
	// Xray stores a VMess account with SecuritySettings added.
	service.users["vm"] = []*protocol.User{{Email: "vm/alice", Account: serial.ToTypedMessage(&vmess.Account{
		Id:               e.ID.String(),
		SecuritySettings: &protocol.SecurityConfig{Type: protocol.SecurityType_AES128_GCM},
	})}}
	session := &Session{handler: service, byTag: map[string]inbound{"vm": in}, log: slog.New(slog.DiscardHandler)}
	st := newState()
	st.Xray["vm/alice"] = e

	if err := session.Apply(t.Context(), st, "vm", []string{"alice"}); err != nil {
		t.Fatal(err)
	}
	if service.alters != 0 {
		t.Errorf("AlterInbound calls = %d, want 0", service.alters)
	}
}

func TestOpenStaleSocketReportsDown(t *testing.T) {
	cfg := newDriftConfig(t.TempDir())
	path := cfg.write(t)
	socket := staleSocket(t)
	dialed := false
	dial := func() (Clients, func(), error) {
		dialed = true
		return Clients{}, func() {}, nil
	}
	if _, err := Open(t.Context(), dial, socket, path, "198.51.100.7", slog.New(slog.DiscardHandler)); err == nil {
		t.Fatal("want an error for a stale socket")
	}
	if dialed {
		t.Fatal("dialed a stale socket")
	}
	if _, err := os.Lstat(socket); !os.IsNotExist(err) {
		t.Fatalf("socket still there: %v", err)
	}
}
