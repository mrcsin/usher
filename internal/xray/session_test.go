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

// userService is a fake Xray HandlerService that keeps the users of each inbound in memory.
type userService struct {
	command.HandlerServiceClient
	running    []*core.InboundHandlerConfig
	users      map[string][]*protocol.User // tag to users
	failAdd    map[string]bool             // emails whose AddUser fails
	failRemove map[string]bool             // emails whose RemoveUser fails
	listErr    error                       // answer of GetInboundUsers
	inboundErr error                       // answer of ListInbounds
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

// credentials maps the emails of the tag to their credentials.
func (f *userService) credentials(t *testing.T, in Inbound) map[string]string {
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

func (f *userService) seed(in Inbound, email string, e state.XrayEntry) {
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
	dial := func() (command.HandlerServiceClient, func(), error) { return service, func() {}, nil }
	session, err := Open(t.Context(), dial, filepath.Join(t.TempDir(), "xray.sock"), path, "198.51.100.7", log)
	if err != nil {
		t.Fatal(err)
	}
	return sessionFixture{session: session, service: service, logs: logs, cfg: cfg}
}

func (f sessionFixture) inbound(tag string) Inbound {
	return f.session.byTag[tag]
}

func newState() *state.State {
	return &state.State{Version: 2, AWG: map[string]state.Entry{}, Xray: map[string]state.XrayEntry{}}
}

func idEntry() state.XrayEntry { return state.XrayEntry{ID: uuid.New()} }

// badInbound is an inbound that fails validation (clients are not empty) and still builds in Xray.
const badInbound = `{"tag": "bad", "protocol": "vless", "port": 9000,
	"settings": {"clients": [{"id": "5783a3e7-e373-51cd-8642-c83782b807c5"}], "decryption": "none"}},`

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

// TestSessionBrokenInboundStillRevokes covers the three ways Prepare keeps a reason instead of
// failing: a validation error, a credential of the wrong kind and a file that cannot be rendered.
func TestSessionBrokenInboundStillRevokes(t *testing.T) {
	notPEM := `{"tag": "pinned", "protocol": "vless", "port": 9003,
		"x-usher": {"trust": "pin"},
		"settings": {"clients": [], "decryption": "none"},
		"streamSettings": {"network": "ws", "security": "tls",
			"tlsSettings": {"serverName": "p.example.com", "certificates": [{"certificate": ["x"], "key": ["y"]}]}}},`
	tests := []struct {
		name    string
		extra   string
		tag     string
		setup   func(st *state.State)
		wantErr string
	}{
		{"validation error", badInbound, "bad", func(*state.State) {}, "settings.clients"},
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
		want  func(st *state.State) map[string]string // email to credential
		alter int                                     // AlterInbound calls expected
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
	// The file now says a different port than the running Xray.
	f.cfg.realityPort = 8444
	path := f.cfg.write(t)
	dial := func() (command.HandlerServiceClient, func(), error) { return f.service, func() {}, nil }
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

// openWithConfig opens a session on the service for the file the config writes.
func openWithConfig(t *testing.T, service *userService, cfg driftConfig) *Session {
	t.Helper()
	dial := func() (command.HandlerServiceClient, func(), error) { return service, func() {}, nil }
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

func TestOpenClosesConnectionWhenDriftCheckFails(t *testing.T) {
	cfg := newDriftConfig(t.TempDir())
	service := newUserService(nil)
	service.inboundErr = errors.New("api down")
	closed := false
	dial := func() (command.HandlerServiceClient, func(), error) { return service, func() { closed = true }, nil }
	_, err := Open(t.Context(), dial, filepath.Join(t.TempDir(), "xray.sock"), cfg.write(t), "198.51.100.7",
		slog.New(slog.DiscardHandler))
	if err == nil || !strings.Contains(err.Error(), "api down") {
		t.Errorf("Open error = %v, want the list error", err)
	}
	if !closed {
		t.Error("Open did not close the connection")
	}
}

func TestOpenDialFails(t *testing.T) {
	cfg := newDriftConfig(t.TempDir())
	dial := func() (command.HandlerServiceClient, func(), error) { return nil, nil, errors.New("no socket") }
	_, err := Open(t.Context(), dial, filepath.Join(t.TempDir(), "xray.sock"), cfg.write(t), "198.51.100.7",
		slog.New(slog.DiscardHandler))
	if err == nil || !strings.Contains(err.Error(), "connecting to xray") {
		t.Errorf("Open error = %v, want the connect error", err)
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
	in := Inbound{Tag: "vm", Protocol: "vmess"}
	e := idEntry()
	service := newUserService(nil)
	// Xray stores a VMess account with SecuritySettings added.
	service.users["vm"] = []*protocol.User{{Email: "vm/alice", Account: serial.ToTypedMessage(&vmess.Account{
		Id:               e.ID.String(),
		SecuritySettings: &protocol.SecurityConfig{Type: protocol.SecurityType_AES128_GCM},
	})}}
	session := &Session{client: service, byTag: map[string]Inbound{"vm": in}, log: slog.New(slog.DiscardHandler)}
	st := newState()
	st.Xray["vm/alice"] = e

	if err := session.Apply(t.Context(), st, "vm", []string{"alice"}); err != nil {
		t.Fatal(err)
	}
	if service.alters != 0 {
		t.Errorf("AlterInbound calls = %d, want 0", service.alters)
	}
}

func TestProtocolCredential(t *testing.T) {
	id := uuid.New()
	tests := []struct {
		name     string
		in       Inbound
		entry    state.XrayEntry
		want     string
		wrongFor string // protocol whose credential function must return "" for this account
	}{
		{"vless vision", Inbound{Protocol: "vless", Network: "raw", Security: "reality"},
			state.XrayEntry{ID: id}, id.String() + " xtls-rprx-vision", "trojan"},
		{"vless ws", Inbound{Protocol: "vless", Network: "ws", Security: "tls"},
			state.XrayEntry{ID: id}, id.String() + " ", "vmess"},
		{"vless raw without security", Inbound{Protocol: "vless", Network: "raw", Security: "none"},
			state.XrayEntry{ID: id}, id.String() + " ", "hysteria"},
		{"vmess", Inbound{Protocol: "vmess"}, state.XrayEntry{ID: id}, id.String(), "vless"},
		{"trojan", Inbound{Protocol: "trojan"}, state.XrayEntry{Password: "pw"}, "pw", "hysteria"},
		{"hysteria", Inbound{Protocol: "hysteria"}, state.XrayEntry{Password: "pw"}, "pw", "trojan"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := protocols[tt.in.Protocol]
			account := spec.account(tt.in, tt.entry)
			if got := spec.credential(account); got != tt.want {
				t.Errorf("credential = %q, want %q", got, tt.want)
			}
			if got := protocols[tt.wrongFor].credential(account); got != "" {
				t.Errorf("%s credential of a %s account = %q, want empty", tt.wrongFor, tt.in.Protocol, got)
			}
		})
	}
}

func TestOpenStaleSocketReportsDown(t *testing.T) {
	cfg := newDriftConfig(t.TempDir())
	path := cfg.write(t)
	socket := staleSocket(t)
	dialed := false
	dial := func() (command.HandlerServiceClient, func(), error) {
		dialed = true
		return nil, func() {}, nil
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
