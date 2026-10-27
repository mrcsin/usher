package awg

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"maps"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	awgv1 "github.com/mrcsin/awg-grpc/gen/awg/v1"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/mrcsin/usher/internal/state"
	"github.com/mrcsin/usher/internal/status"
)

type sessionEnv struct {
	t      *testing.T
	client *fakeClient
	logs   *bytes.Buffer
	ifaces []*awgv1.InterfaceStatus
	st     *state.State
}

func newSessionEnv(t *testing.T, ifaces ...*awgv1.InterfaceStatus) *sessionEnv {
	t.Helper()
	e := &sessionEnv{t: t, logs: &bytes.Buffer{}, ifaces: ifaces, st: &state.State{AWG: map[string]state.Entry{}}}
	e.client = &fakeClient{status: func() (*awgv1.GetStatusResponse, error) {
		return &awgv1.GetStatusResponse{Interfaces: e.ifaces}, nil
	}}
	return e
}

func (e *sessionEnv) open() *Session {
	e.t.Helper()
	dial := func() (awgv1.ManagementServiceClient, func(), error) { return e.client, func() {}, nil }
	s, err := Open(context.Background(), dial, netip.MustParseAddr("203.0.113.10"),
		[]netip.Addr{netip.MustParseAddr("1.1.1.1")}, slog.New(slog.NewTextHandler(e.logs, nil)))
	if err != nil {
		e.t.Fatal(err)
	}
	return s
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

func TestOpen(t *testing.T) {
	tests := []struct {
		name    string
		dial    Dial
		wantErr string
	}{
		{"dial error", func() (awgv1.ManagementServiceClient, func(), error) { return nil, nil, errors.New("no socket") },
			"connecting to awg-grpc"},
		{"get status error", func() (awgv1.ManagementServiceClient, func(), error) {
			c := &fakeClient{status: func() (*awgv1.GetStatusResponse, error) { return nil, errors.New("down") }}
			return c, func() {}, nil
		}, "getting status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Open(context.Background(), tt.dial, netip.MustParseAddr("203.0.113.10"), nil, slog.New(slog.DiscardHandler))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
	t.Run("lists the interfaces in reported order", func(t *testing.T) {
		e := newSessionEnv(t, named("awg1", "10.9.1.1/24"), named("awg0", "10.8.1.1/24"))
		if got := e.open().Interfaces(); !slices.Equal(got, []string{"awg1", "awg0"}) {
			t.Errorf("Interfaces = %v", got)
		}
	})
}

func TestSessionRequestShape(t *testing.T) {
	e := newSessionEnv(t, named("awg0", "10.8.1.1/24"), named("awg1", "10.9.1.1/24"))
	s := e.open()
	users := map[string][]string{"awg0": {"laptop", "phone"}, "awg1": nil}

	for _, name := range s.Interfaces() {
		if _, _, err := s.Prepare(e.st, name, users[name]); err != nil {
			t.Fatal(err)
		}
		if err := s.Apply(context.Background(), e.st, name, users[name]); err != nil {
			t.Fatal(err)
		}
	}

	if len(e.client.applied) != 2 {
		t.Fatalf("requests = %d, want 2", len(e.client.applied))
	}
	first, second := e.client.applied[0], e.client.applied[1]
	if first.GetInterfaceName() != "awg0" || first.GetAllowEmpty() || len(first.GetPeers()) != 2 {
		t.Fatalf("awg0 request: allow_empty=%v peers=%d", first.GetAllowEmpty(), len(first.GetPeers()))
	}
	for i, user := range users["awg0"] {
		p := first.GetPeers()[i]
		want := e.st.AWG["awg0/"+user]
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
	if second.GetInterfaceName() != "awg1" || !second.GetAllowEmpty() || len(second.GetPeers()) != 0 {
		t.Errorf("awg1 request: allow_empty=%v peers=%d", second.GetAllowEmpty(), len(second.GetPeers()))
	}
}

func TestPrepare(t *testing.T) {
	t.Run("renders one file per user and reports new entries", func(t *testing.T) {
		e := newSessionEnv(t, named("awg0", "10.8.1.1/24"))
		s := e.open()

		files, changed, err := s.Prepare(e.st, "awg0", []string{"laptop", "phone"})
		if err != nil || !changed {
			t.Fatalf("changed=%v err=%v", changed, err)
		}
		if got := slices.Sorted(maps.Keys(files)); !slices.Equal(got, []string{"laptop/awg0.conf", "phone/awg0.conf"}) {
			t.Errorf("files = %v", got)
		}
		if !strings.Contains(string(files["phone/awg0.conf"]), "[Peer]") {
			t.Errorf("phone config has no peer section")
		}

		_, changed, err = s.Prepare(e.st, "awg0", []string{"laptop", "phone"})
		if err != nil || changed {
			t.Errorf("second prepare: changed=%v err=%v", changed, err)
		}
	})
	missing := named("awg0", "10.8.1.1/24")
	missing.Present = false
	tests := []struct {
		name  string
		iface *awgv1.InterfaceStatus
	}{
		{"not present", missing},
		{"no free address", named("awg0", "10.8.1.1/30")},
		{"render error", named("awg0", "10.8.1.1/24", &awgv1.ConfigParam{Key: "S1", Value: "a\nb"})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newSessionEnv(t, tt.iface)
			if _, _, err := e.open().Prepare(e.st, "awg0", []string{"phone", "laptop"}); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestApplyError(t *testing.T) {
	e := newSessionEnv(t, named("awg0", "10.8.1.1/24"))
	e.client.apply = func(*awgv1.ApplyPeersRequest) (*awgv1.ApplyPeersResponse, error) {
		return nil, errors.New("rejected")
	}
	s := e.open()
	if _, _, err := s.Prepare(e.st, "awg0", []string{"phone"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(context.Background(), e.st, "awg0", []string{"phone"}); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("err = %v", err)
	}
}

func TestApplyLogsResult(t *testing.T) {
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
			e := newSessionEnv(t, named("awg0", "10.8.1.1/24"))
			e.client.apply = func(r *awgv1.ApplyPeersRequest) (*awgv1.ApplyPeersResponse, error) {
				if tt.response != nil {
					return tt.response, nil
				}
				return &awgv1.ApplyPeersResponse{Added: [][]byte{r.GetPeers()[0].GetPublicKey()}}, nil
			}
			s := e.open()
			if _, _, err := s.Prepare(e.st, "awg0", []string{"phone"}); err != nil {
				t.Fatal(err)
			}

			if err := s.Apply(context.Background(), e.st, "awg0", []string{"phone"}); err != nil {
				t.Fatal(err)
			}

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

func TestApplySwitchOff(t *testing.T) {
	e := newSessionEnv(t, named("awg0", "10.8.1.1/24"))
	s := e.open()
	if _, _, err := s.Prepare(e.st, "awg0", []string{"laptop", "phone"}); err != nil {
		t.Fatal(err)
	}
	laptop := e.st.AWG["awg0/laptop"]
	e.client.apply = func(*awgv1.ApplyPeersRequest) (*awgv1.ApplyPeersResponse, error) {
		return &awgv1.ApplyPeersResponse{Removed: [][]byte{laptop.PublicKey()}}, nil
	}

	if err := s.Apply(context.Background(), e.st, "awg0", []string{"phone"}); err != nil {
		t.Fatal(err)
	}

	if got := e.client.applied[0]; len(got.GetPeers()) != 1 {
		t.Fatalf("peers = %d, want 1", len(got.GetPeers()))
	}
	if !strings.Contains(e.logs.String(), "removed=[laptop]") {
		t.Errorf("log does not name the removed user: %s", e.logs)
	}
}

func TestLogHasNoSecrets(t *testing.T) {
	const headerKey = "HDRKEYSECRET"
	const leak = "LEAKSECRET"
	e := newSessionEnv(t,
		named("awg0", "10.8.1.1/24", &awgv1.ConfigParam{Key: "HeaderProtectionKey", Value: headerKey}),
		named("awg1", "10.9.1.1/24", &awgv1.ConfigParam{Key: "S1", Value: "x\n" + leak}))
	e.client.apply = func(r *awgv1.ApplyPeersRequest) (*awgv1.ApplyPeersResponse, error) {
		return &awgv1.ApplyPeersResponse{Added: [][]byte{r.GetPeers()[0].GetPublicKey()}}, nil
	}
	s := e.open()

	for _, name := range s.Interfaces() {
		_, _, err := s.Prepare(e.st, name, []string{"phone"})
		if err != nil {
			e.logs.WriteString(err.Error())
			continue
		}
		if err := s.Apply(context.Background(), e.st, name, []string{"phone"}); err != nil {
			t.Fatal(err)
		}
	}

	logs := e.logs.String()
	if logs == "" {
		t.Fatal("no log output")
	}
	secrets := []string{headerKey, leak}
	for _, entry := range e.st.AWG {
		secrets = append(secrets, entry.PrivateKey.String(), entry.PresharedKey.String())
	}
	for _, s := range secrets {
		if strings.Contains(logs, s) {
			t.Errorf("log contains a secret: %q", s)
		}
	}
}

func TestPeerStats(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	key := func(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }
	names := map[string]string{string(key(1)): "phone", string(key(2)): "laptop", string(key(3)): "tablet"}
	peer := func(k []byte, ago time.Duration, rx, tx uint64) *awgv1.PeerStatus {
		return &awgv1.PeerStatus{PublicKey: k, RxBytes: rx, TxBytes: tx, LastHandshake: timestamppb.New(now.Add(-ago))}
	}
	tests := []struct {
		name  string
		peers []*awgv1.PeerStatus
		users []string
		want  map[string]status.UserStats
	}{
		{"never handshaked", []*awgv1.PeerStatus{{PublicKey: key(1)}}, []string{"phone"},
			map[string]status.UserStats{"phone": {}}},
		{"handshake in the future", []*awgv1.PeerStatus{peer(key(1), -30*time.Second, 10, 20)}, []string{"phone"},
			map[string]status.UserStats{"phone": {Uplink: 10, Downlink: 20}}},
		{"handshake exactly 180 s ago", []*awgv1.PeerStatus{peer(key(1), 180*time.Second, 10, 20)}, []string{"phone"},
			map[string]status.UserStats{"phone": {Uplink: 10, Downlink: 20}}},
		{"handshake 179 s ago", []*awgv1.PeerStatus{peer(key(1), 179*time.Second, 10, 20)}, []string{"phone"},
			map[string]status.UserStats{"phone": {Uplink: 10, Downlink: 20, Online: true}}},
		{"handshake 181 s ago", []*awgv1.PeerStatus{peer(key(1), 181*time.Second, 10, 20)}, []string{"phone"},
			map[string]status.UserStats{"phone": {Uplink: 10, Downlink: 20}}},
		{"peer of a user not requested", []*awgv1.PeerStatus{peer(key(1), 0, 1, 2), peer(key(2), 0, 3, 4)}, []string{"phone"},
			map[string]status.UserStats{"phone": {Uplink: 1, Downlink: 2, Online: true}}},
		{"foreign key", []*awgv1.PeerStatus{peer(key(7), 0, 1, 2)}, []string{"phone"},
			map[string]status.UserStats{}},
		{"requested user without a peer", []*awgv1.PeerStatus{peer(key(1), 0, 1, 2)}, []string{"phone", "tablet"},
			map[string]status.UserStats{"phone": {Uplink: 1, Downlink: 2, Online: true}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := peerStats(tt.peers, names, tt.users, now); !maps.Equal(got, tt.want) {
				t.Errorf("peerStats = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStats(t *testing.T) {
	t.Run("maps the listed peers to users", func(t *testing.T) {
		e := newSessionEnv(t, named("awg0", "10.8.1.1/24"))
		s := e.open()
		if _, _, err := s.Prepare(e.st, "awg0", []string{"phone"}); err != nil {
			t.Fatal(err)
		}
		var requested string
		e.client.listPeers = func(r *awgv1.ListPeersRequest) (*awgv1.ListPeersResponse, error) {
			requested = r.GetInterfaceName()
			return &awgv1.ListPeersResponse{Peers: []*awgv1.PeerStatus{{
				PublicKey:     e.st.AWG["awg0/phone"].PublicKey(),
				LastHandshake: timestamppb.Now(),
				RxBytes:       100,
				TxBytes:       200,
			}}}, nil
		}

		got, err := s.Stats(context.Background(), e.st, "awg0", []string{"phone"})
		if err != nil {
			t.Fatal(err)
		}
		if requested != "awg0" {
			t.Errorf("requested interface = %q", requested)
		}
		want := map[string]status.UserStats{"phone": {Uplink: 100, Downlink: 200, Online: true}}
		if !maps.Equal(got, want) {
			t.Errorf("Stats = %v, want %v", got, want)
		}
	})
	t.Run("returns a ListPeers error wrapped", func(t *testing.T) {
		e := newSessionEnv(t, named("awg0", "10.8.1.1/24"))
		s := e.open()
		down := errors.New("down")
		e.client.listPeers = func(*awgv1.ListPeersRequest) (*awgv1.ListPeersResponse, error) { return nil, down }

		_, err := s.Stats(context.Background(), e.st, "awg0", []string{"phone"})
		if !errors.Is(err, down) || !strings.Contains(err.Error(), "listing peers") {
			t.Fatalf("err = %v", err)
		}
	})
}
