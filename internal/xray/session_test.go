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

	"github.com/mrcsin/usher/internal/state"
)

// userService is a fake Xray HandlerService that keeps the users of each inbound in memory.
type userService struct {
	command.HandlerServiceClient
	running []*core.InboundHandlerConfig
	users   map[string][]*protocol.User // tag to users
	failAdd map[string]bool             // emails whose AddUser fails
	alters  int
}

func newUserService(running []*core.InboundHandlerConfig) *userService {
	return &userService{running: running, users: map[string][]*protocol.User{}, failAdd: map[string]bool{}}
}

func (f *userService) ListInbounds(_ context.Context, _ *command.ListInboundsRequest,
	_ ...grpc.CallOption) (*command.ListInboundsResponse, error) {
	return &command.ListInboundsResponse{Inbounds: f.running}, nil
}

func (f *userService) GetInboundUsers(_ context.Context, req *command.GetInboundUserRequest,
	_ ...grpc.CallOption) (*command.GetInboundUserResponse, error) {
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
	f.users[in.Tag] = append(f.users[in.Tag], &protocol.User{Email: email, Account: typedAccount(in, e)})
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
	service := newUserService(runningValid(t, path))
	logs := &bytes.Buffer{}
	log := slog.New(slog.NewTextHandler(logs, nil))
	dial := func() (command.HandlerServiceClient, func(), error) { return service, func() {}, nil }
	session, err := Open(t.Context(), dial, filepath.Join(t.TempDir(), "xray.sock"), path, log)
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

func TestSessionInterfaces(t *testing.T) {
	f := newSessionFixture(t, func(c *driftConfig) {
		c.extra = `{"tag": "bad", "protocol": "vless", "port": 9000, "settings": {"clients": [{"id": "x"}]}},`
	})
	if got, want := f.session.Interfaces(), []string{"bad", "reality", "ws", "hy2"}; !slices.Equal(got, want) {
		t.Errorf("Interfaces() = %v, want %v", got, want)
	}
	if _, _, err := f.session.Prepare(newState(), "bad", []string{"alice"}); err == nil {
		t.Error("Prepare on an invalid inbound succeeded")
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
	if !changed || files == nil {
		t.Errorf("changed = %v, files = %v, want changed and a non-nil map", changed, files)
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
			logs:  []string{"users changed", "added", "reality/alice"},
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
			logs:  []string{"removed", "hy2/carol", "stranger"},
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
			logs:  []string{"updated", "hy2/alice"},
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
	session, err := Open(t.Context(), dial, filepath.Join(t.TempDir(), "xray.sock"), path, slog.New(slog.DiscardHandler))
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

// runningValid builds the valid inbounds of the file the way Xray does at start.
func runningValid(t *testing.T, path string) []*core.InboundHandlerConfig {
	t.Helper()
	inbounds, err := ReadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	var running []*core.InboundHandlerConfig
	for _, in := range inbounds {
		if in.Err != nil {
			continue
		}
		built, err := in.Detour.Build()
		if err != nil {
			t.Fatal(err)
		}
		running = append(running, built)
	}
	return running
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
	if _, err := Open(t.Context(), dial, socket, path, slog.New(slog.DiscardHandler)); err == nil {
		t.Fatal("want an error for a stale socket")
	}
	if dialed {
		t.Fatal("dialed a stale socket")
	}
	if _, err := os.Lstat(socket); !os.IsNotExist(err) {
		t.Fatalf("socket still there: %v", err)
	}
}
