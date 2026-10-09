package xray

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"testing"
	"uuid"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	"github.com/mrcsin/usher/gen/xray/app/proxyman/command"
	userproto "github.com/mrcsin/usher/gen/xray/common/protocol"
	"github.com/mrcsin/usher/gen/xray/common/serial"
	"github.com/mrcsin/usher/gen/xray/core"
	vlessaccount "github.com/mrcsin/usher/gen/xray/proxy/vless"
	"github.com/mrcsin/usher/internal/state"
)

// fakeXray implements command.HandlerServiceClient over in-memory inbounds. It applies the
// operations it gets, so a second pass sees the result of the first.
type fakeXray struct {
	command.HandlerServiceClient

	configs []*core.InboundHandlerConfig
	users   map[string]map[string]*serial.TypedMessage
	// failAdd names an email whose AddUserOperation fails.
	failAdd string
	calls   []string
}

func newFakeXray(configs ...*core.InboundHandlerConfig) *fakeXray {
	f := &fakeXray{configs: configs, users: map[string]map[string]*serial.TypedMessage{}}
	for _, c := range configs {
		f.users[c.GetTag()] = map[string]*serial.TypedMessage{}
	}
	return f
}

func (f *fakeXray) ListInbounds(context.Context, *command.ListInboundsRequest, ...grpc.CallOption) (*command.ListInboundsResponse, error) {
	return &command.ListInboundsResponse{Inbounds: f.configs}, nil
}

func (f *fakeXray) GetInboundUsers(_ context.Context, in *command.GetInboundUserRequest, _ ...grpc.CallOption) (*command.GetInboundUserResponse, error) {
	var response command.GetInboundUserResponse
	for _, email := range slices.Sorted(maps.Keys(f.users[in.GetTag()])) {
		response.Users = append(response.Users, &userproto.User{Email: email, Account: f.users[in.GetTag()][email]})
	}
	return &response, nil
}

func (f *fakeXray) AlterInbound(_ context.Context, in *command.AlterInboundRequest, _ ...grpc.CallOption) (*command.AlterInboundResponse, error) {
	operation := in.GetOperation()
	switch operation.GetType() {
	case typeName(&command.AddUserOperation{}):
		var add command.AddUserOperation
		if err := proto.Unmarshal(operation.GetValue(), &add); err != nil {
			return nil, err
		}
		email := add.GetUser().GetEmail()
		f.calls = append(f.calls, "add "+email)
		if email == f.failAdd {
			return nil, errors.New("xray refused " + email)
		}
		f.users[in.GetTag()][email] = add.GetUser().GetAccount()
	case typeName(&command.RemoveUserOperation{}):
		var remove command.RemoveUserOperation
		if err := proto.Unmarshal(operation.GetValue(), &remove); err != nil {
			return nil, err
		}
		f.calls = append(f.calls, "remove "+remove.GetEmail())
		delete(f.users[in.GetTag()], remove.GetEmail())
	default:
		return nil, errors.New("unexpected operation " + operation.GetType())
	}
	return &command.AlterInboundResponse{}, nil
}

type sessionEnv struct {
	t    *testing.T
	xray *fakeXray
	logs *bytes.Buffer
	st   *state.State
}

func newSessionEnv(t *testing.T, xray *fakeXray) *sessionEnv {
	t.Helper()
	return &sessionEnv{t: t, xray: xray, logs: &bytes.Buffer{}, st: &state.State{Xray: map[string]state.XrayEntry{}}}
}

func (e *sessionEnv) open() *Session {
	e.t.Helper()
	dial := func() (command.HandlerServiceClient, func(), error) { return e.xray, func() {}, nil }
	s, err := Open(context.Background(), dial, slog.New(slog.NewTextHandler(e.logs, nil)))
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

// pass runs Prepare and Apply for one inbound, as the pass does.
func (e *sessionEnv) pass(name string, users ...string) error {
	e.t.Helper()
	s := e.open()
	defer s.Close()
	if _, _, err := s.Prepare(e.st, name, users); err != nil {
		return err
	}
	return s.Apply(context.Background(), e.st, name, users)
}

func (e *sessionEnv) accountOf(tag, user string) *serial.TypedMessage {
	return e.xray.users[tag][state.EntryName(tag, user)]
}

func vlessAccount(id uuid.UUID, flow string) *serial.TypedMessage {
	return toTypedMessage(&vlessaccount.Account{Id: id.String(), Flow: flow})
}

func twoInbounds(t *testing.T) *fakeXray {
	t.Helper()
	return newFakeXray(
		inboundConfig("vless", portList(443, 443), realityStream(t)),
		inboundConfig("spare", portList(8443, 8443), realityStream(t)),
	)
}

func TestOpen(t *testing.T) {
	t.Run("lists inbound tags in order", func(t *testing.T) {
		env := newSessionEnv(t, twoInbounds(t))
		s := env.open()
		if got := s.Interfaces(); !slices.Equal(got, []string{"vless", "spare"}) {
			t.Errorf("Interfaces = %v", got)
		}
	})

	t.Run("dial error", func(t *testing.T) {
		dial := func() (command.HandlerServiceClient, func(), error) { return nil, nil, errors.New("no socket") }
		if _, err := Open(context.Background(), dial, slog.Default()); err == nil || !strings.Contains(err.Error(), "no socket") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("list error closes the connection", func(t *testing.T) {
		closed := false
		dial := func() (command.HandlerServiceClient, func(), error) {
			return &failingList{}, func() { closed = true }, nil
		}
		if _, err := Open(context.Background(), dial, slog.Default()); err == nil {
			t.Fatal("no error")
		}
		if !closed {
			t.Error("connection stays open")
		}
	})
}

type failingList struct{ command.HandlerServiceClient }

func (*failingList) ListInbounds(context.Context, *command.ListInboundsRequest, ...grpc.CallOption) (*command.ListInboundsResponse, error) {
	return nil, errors.New("unavailable")
}

func TestApplyAddsAndRemoves(t *testing.T) {
	env := newSessionEnv(t, twoInbounds(t))
	if err := env.pass("vless", "alice", "bob"); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"alice", "bob"} {
		want := vlessAccount(env.st.Xray["vless/"+user].ID, flowVision)
		if got := env.accountOf("vless", user); !sameAccount(got, want) {
			t.Errorf("%s account = %v, want %v", user, got, want)
		}
	}
	if !strings.Contains(env.logs.String(), "users changed") || !strings.Contains(env.logs.String(), "added=\"[alice bob]\"") {
		t.Errorf("logs = %q", env.logs.String())
	}

	env.logs.Reset()
	env.xray.calls = nil
	if err := env.pass("vless", "alice"); err != nil {
		t.Fatal(err)
	}
	if got := slices.Collect(maps.Keys(env.xray.users["vless"])); !slices.Equal(got, []string{"vless/alice"}) {
		t.Errorf("users = %v", got)
	}
	if !slices.Equal(env.xray.calls, []string{"remove vless/bob"}) {
		t.Errorf("calls = %v", env.xray.calls)
	}
	if !strings.Contains(env.logs.String(), "removed=[bob]") {
		t.Errorf("logs = %q", env.logs.String())
	}

	env.logs.Reset()
	env.xray.calls = nil
	if err := env.pass("vless", "alice"); err != nil {
		t.Fatal(err)
	}
	if len(env.xray.calls) != 0 || env.logs.Len() != 0 {
		t.Errorf("an unchanged pass made calls %v and logged %q", env.xray.calls, env.logs.String())
	}
}

func TestApplyEmptiesUnreferencedInbound(t *testing.T) {
	env := newSessionEnv(t, twoInbounds(t))
	if err := env.pass("spare", "alice"); err != nil {
		t.Fatal(err)
	}
	if err := env.pass("spare"); err != nil {
		t.Fatal(err)
	}
	if len(env.xray.users["spare"]) != 0 {
		t.Errorf("users = %v", env.xray.users["spare"])
	}
}

func TestApplyKeepsSeededUUID(t *testing.T) {
	seeded := uuid.MustParse("5f0c3a52-4a6e-4d0b-8c1d-7e9f2b3a4c5d")
	env := newSessionEnv(t, twoInbounds(t))
	env.st.Xray["vless/alice"] = state.XrayEntry{ID: seeded}
	if err := env.pass("vless", "alice"); err != nil {
		t.Fatal(err)
	}
	if got := env.st.Xray["vless/alice"].ID; got != seeded {
		t.Errorf("id = %v", got)
	}
	if got := env.accountOf("vless", "alice"); !sameAccount(got, vlessAccount(seeded, flowVision)) {
		t.Errorf("account = %v", got)
	}
}

func TestApplyReplacesChangedAccount(t *testing.T) {
	tests := []struct {
		name    string
		account *serial.TypedMessage
	}{
		{name: "flow change", account: vlessAccount(uuid.MustParse("5f0c3a52-4a6e-4d0b-8c1d-7e9f2b3a4c5d"), "")},
		{name: "id change", account: vlessAccount(uuid.NewV4(), flowVision)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newSessionEnv(t, twoInbounds(t))
			env.st.Xray["vless/alice"] = state.XrayEntry{ID: uuid.MustParse("5f0c3a52-4a6e-4d0b-8c1d-7e9f2b3a4c5d")}
			env.xray.users["vless"]["vless/alice"] = tt.account

			if err := env.pass("vless", "alice"); err != nil {
				t.Fatal(err)
			}
			want := vlessAccount(env.st.Xray["vless/alice"].ID, flowVision)
			if got := env.accountOf("vless", "alice"); !sameAccount(got, want) {
				t.Errorf("account = %v, want %v", got, want)
			}
			if want := []string{"remove vless/alice", "add vless/alice"}; !slices.Equal(env.xray.calls, want) {
				t.Errorf("calls = %v, want %v", env.xray.calls, want)
			}
			if !strings.Contains(env.logs.String(), "updated=[alice]") {
				t.Errorf("logs = %q", env.logs.String())
			}
		})
	}
}

func TestSameAccount(t *testing.T) {
	id := uuid.MustParse("5f0c3a52-4a6e-4d0b-8c1d-7e9f2b3a4c5d")
	// An explicit zero in field 5 (seconds) is the same message with a different encoding.
	explicit := &serial.TypedMessage{
		Type:  typeName(&vlessaccount.Account{}),
		Value: append(vlessAccount(id, flowVision).GetValue(), 0x28, 0x00),
	}
	if !sameAccount(explicit, vlessAccount(id, flowVision)) {
		t.Error("equal accounts differ")
	}
	if sameAccount(&serial.TypedMessage{Type: "unknown.Type"}, &serial.TypedMessage{Type: "unknown.Type"}) {
		t.Error("an unknown type equals itself")
	}
}

func TestApplyConvergesAfterPartialFailure(t *testing.T) {
	env := newSessionEnv(t, twoInbounds(t))
	env.xray.failAdd = "vless/bob"

	err := env.pass("vless", "alice", "bob")
	if err == nil || !strings.Contains(err.Error(), "adding user bob") {
		t.Fatalf("error = %v", err)
	}
	if env.accountOf("vless", "alice") == nil || env.accountOf("vless", "bob") != nil {
		t.Fatalf("users = %v", slices.Collect(maps.Keys(env.xray.users["vless"])))
	}

	env.xray.failAdd = ""
	if err := env.pass("vless", "alice", "bob"); err != nil {
		t.Fatal(err)
	}
	if env.accountOf("vless", "bob") == nil {
		t.Error("bob is missing after the next pass")
	}
}

func TestPrepareFailsUndecodableInbound(t *testing.T) {
	websocket := realityStream(t)
	websocket.ProtocolName = "websocket"
	env := newSessionEnv(t, newFakeXray(inboundConfig("bad", portList(443, 443), websocket)))
	s := env.open()

	_, changed, err := s.Prepare(env.st, "bad", []string{"alice"})
	if err == nil || !strings.Contains(err.Error(), `transport "websocket"`) || changed {
		t.Fatalf("Prepare = %v, %v", changed, err)
	}
	if len(env.st.Xray) != 0 {
		t.Errorf("state = %v", env.st.Xray)
	}
	if err := s.Apply(context.Background(), env.st, "bad", []string{"alice"}); err == nil {
		t.Error("Apply succeeded on a failed inbound")
	}
}

func TestPrepareReportsEnrollment(t *testing.T) {
	env := newSessionEnv(t, twoInbounds(t))
	s := env.open()
	_, changed, err := s.Prepare(env.st, "vless", []string{"alice"})
	if err != nil || !changed {
		t.Fatalf("first Prepare = %v, %v", changed, err)
	}
	_, changed, err = s.Prepare(env.st, "vless", []string{"alice"})
	if err != nil || changed {
		t.Fatalf("second Prepare = %v, %v", changed, err)
	}
}

func TestSessionNeverLogsUUID(t *testing.T) {
	env := newSessionEnv(t, twoInbounds(t))
	env.xray.failAdd = "vless/bob"
	err := env.pass("vless", "alice", "bob")
	if err == nil {
		t.Fatal("no error")
	}
	env.xray.failAdd = ""
	if err := env.pass("vless", "alice", "bob"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uuid.UUID{env.st.Xray["vless/alice"].ID, env.st.Xray["vless/bob"].ID} {
		text := id.String()
		if strings.Contains(env.logs.String(), text) || strings.Contains(err.Error(), text) {
			t.Errorf("a log line or an error contains uuid %s", text)
		}
	}
}
