package pass

import (
	"bytes"
	"log/slog"
	"maps"
	"net/netip"
	"strings"
	"testing"

	awgv1 "github.com/mrcsin/awg-grpc/gen/awg/v1"

	"github.com/mrcsin/usher/internal/state"
)

func ifaceStatus(addresses ...string) *awgv1.InterfaceStatus {
	return &awgv1.InterfaceStatus{Name: "awg0", Present: true, Addresses: addresses}
}

func stateWith(entries map[string]string) *state.State {
	st := &state.State{Version: 1, AWG: map[string]state.Entry{}}
	for name, addr := range entries {
		st.AWG[name] = state.Entry{
			Address:      netip.MustParseAddr(addr),
			PrivateKey:   state.Key(bytes.Repeat([]byte{1}, state.KeyLength)),
			PresharedKey: state.Key(bytes.Repeat([]byte{2}, state.KeyLength)),
		}
	}
	return st
}

func TestEnroll(t *testing.T) {
	tests := []struct {
		name      string
		iface     *awgv1.InterfaceStatus
		entries   map[string]string
		users     []string
		want      map[string]string
		wantErr   string
		wantLog   string
		unchanged bool
	}{
		{
			name:  "lowest free address",
			iface: ifaceStatus("10.8.1.1/24"),
			users: []string{"laptop", "phone"},
			want:  map[string]string{"awg0/laptop": "10.8.1.2", "awg0/phone": "10.8.1.3"},
		},
		{
			name:  "interface address in the middle is skipped",
			iface: ifaceStatus("10.8.1.2/24"),
			users: []string{"a", "b"},
			want:  map[string]string{"awg0/a": "10.8.1.1", "awg0/b": "10.8.1.3"},
		},
		{
			name:  "network address is skipped",
			iface: ifaceStatus("10.8.1.1/24"),
			users: []string{"a"},
			want:  map[string]string{"awg0/a": "10.8.1.2"},
		},
		{
			name:    "broadcast address is never used",
			iface:   ifaceStatus("10.8.1.1/30"),
			users:   []string{"a", "b"},
			wantErr: "no free address",
		},
		{
			name:    "switched-off user keeps its address taken",
			iface:   ifaceStatus("10.8.1.1/24"),
			entries: map[string]string{"awg0/guest": "10.8.1.2"},
			users:   []string{"phone"},
			want:    map[string]string{"awg0/guest": "10.8.1.2", "awg0/phone": "10.8.1.3"},
		},
		{
			name:      "valid stored address stays",
			iface:     ifaceStatus("10.8.1.1/24"),
			entries:   map[string]string{"awg0/phone": "10.8.1.9"},
			users:     []string{"phone"},
			want:      map[string]string{"awg0/phone": "10.8.1.9"},
			unchanged: true,
		},
		{
			name:    "prefix moved",
			iface:   ifaceStatus("10.9.0.1/24"),
			entries: map[string]string{"awg0/phone": "10.8.1.9"},
			users:   []string{"phone"},
			want:    map[string]string{"awg0/phone": "10.9.0.2"},
			wantLog: "reassigned address",
		},
		{
			name:    "interface address moved onto a held address",
			iface:   ifaceStatus("10.8.1.2/24"),
			entries: map[string]string{"awg0/phone": "10.8.1.2"},
			users:   []string{"phone"},
			want:    map[string]string{"awg0/phone": "10.8.1.1"},
			wantLog: "reassigned address",
		},
		{
			name:    "shrink turns a held address into the broadcast address",
			iface:   ifaceStatus("10.8.1.1/25"),
			entries: map[string]string{"awg0/phone": "10.8.1.127"},
			users:   []string{"phone"},
			want:    map[string]string{"awg0/phone": "10.8.1.2"},
			wantLog: "reassigned address",
		},
		{
			name:    "duplicate address is reassigned",
			iface:   ifaceStatus("10.8.1.1/24"),
			entries: map[string]string{"awg0/a": "10.8.1.5", "awg0/b": "10.8.1.5"},
			users:   []string{"a", "b"},
			want:    map[string]string{"awg0/a": "10.8.1.5", "awg0/b": "10.8.1.2"},
			wantLog: "reassigned address",
		},
		{
			name:    "new user sorting first leaves an existing address alone",
			iface:   ifaceStatus("10.8.1.1/24"),
			entries: map[string]string{"awg0/bob": "10.8.1.2"},
			users:   []string{"alice", "bob"},
			want:    map[string]string{"awg0/alice": "10.8.1.3", "awg0/bob": "10.8.1.2"},
		},
		{
			name:    "other interface entries are ignored",
			iface:   ifaceStatus("10.8.1.1/24"),
			entries: map[string]string{"awg1/phone": "10.8.1.2"},
			users:   []string{"phone"},
			want:    map[string]string{"awg1/phone": "10.8.1.2", "awg0/phone": "10.8.1.2"},
		},
		{
			name:    "no free address",
			iface:   ifaceStatus("10.8.1.1/30"),
			entries: map[string]string{"awg0/guest": "10.8.1.2"},
			users:   []string{"phone"},
			wantErr: "no free address",
		},
		{
			name:    "reassignment before a failure leaves the state alone",
			iface:   ifaceStatus("10.8.1.1/30"),
			entries: map[string]string{"awg0/a": "10.8.1.200"},
			users:   []string{"a", "b"},
			wantErr: "no free address",
		},
		{
			name:    "a /31 has no host address",
			iface:   ifaceStatus("10.8.1.0/31"),
			users:   []string{"a"},
			wantErr: "no free address",
		},
		{
			name:    "a /32 has no host address",
			iface:   ifaceStatus("10.8.1.1/32"),
			users:   []string{"a"},
			wantErr: "no free address",
		},
		{
			name:    "no IPv4 prefix",
			iface:   ifaceStatus("fd00::1/64"),
			users:   []string{"phone"},
			wantErr: "no IPv4 address",
		},
		{
			name:      "no users",
			iface:     ifaceStatus(),
			users:     nil,
			want:      map[string]string{},
			unchanged: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := stateWith(tc.entries)
			before := maps.Clone(st.AWG)
			var logBuf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&logBuf, nil))

			changed, err := enroll(log, st, tc.iface, tc.users)

			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
				}
				if changed || !maps.Equal(st.AWG, before) {
					t.Fatalf("failed enrollment changed the state: changed=%v state=%v, was %v", changed, st.AWG, before)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if changed == tc.unchanged {
				t.Fatalf("changed = %v, want %v", changed, !tc.unchanged)
			}
			if len(st.AWG) != len(tc.want) {
				t.Fatalf("entries = %d, want %d", len(st.AWG), len(tc.want))
			}
			for name, addr := range tc.want {
				if got := st.AWG[name].Address.String(); got != addr {
					t.Errorf("%s address = %q, want %q", name, got, addr)
				}
				if old, existed := before[name]; existed &&
					(st.AWG[name].PrivateKey != old.PrivateKey || st.AWG[name].PresharedKey != old.PresharedKey) {
					t.Errorf("%s keys changed", name)
				}
			}
			if tc.wantLog != "" && !strings.Contains(logBuf.String(), tc.wantLog) {
				t.Errorf("log %q lacks %q", logBuf.String(), tc.wantLog)
			}
			if tc.wantLog == "" && logBuf.Len() > 0 {
				t.Errorf("unexpected log %q", logBuf.String())
			}
		})
	}
}

func TestEnrollNewEntryKeys(t *testing.T) {
	st := stateWith(nil)
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))

	if _, err := enroll(log, st, ifaceStatus("10.8.1.1/24"), []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}

	pubs := map[string]bool{}
	psks := map[state.Key]bool{}
	for _, e := range st.AWG {
		pubs[string(e.PublicKey())] = true
		psks[e.PresharedKey] = true
	}
	if len(pubs) != 2 || len(psks) != 2 {
		t.Fatalf("keys repeat: %d public, %d preshared", len(pubs), len(psks))
	}
	for name, e := range st.AWG {
		if e.PrivateKey == (state.Key{}) || e.PresharedKey == (state.Key{}) {
			t.Errorf("%s has an empty key", name)
		}
	}
}
