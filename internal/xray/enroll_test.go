package xray

import (
	"encoding/base64"
	"maps"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/mrcsin/usher/internal/state"
)

func TestEnroll(t *testing.T) {
	seededID := uuid.New()
	tests := []struct {
		name        string
		in          Inbound
		seed        map[string]state.XrayEntry
		users       []string
		wantChanged bool
		wantErr     string
	}{
		{name: "vless gets ids", in: Inbound{Tag: "t", Protocol: "vless"}, users: []string{"alice", "bob"}, wantChanged: true},
		{name: "vmess gets ids", in: Inbound{Tag: "t", Protocol: "vmess"}, users: []string{"alice"}, wantChanged: true},
		{name: "trojan gets passwords", in: Inbound{Tag: "t", Protocol: "trojan"}, users: []string{"alice"}, wantChanged: true},
		{name: "hysteria gets passwords", in: Inbound{Tag: "t", Protocol: "hysteria"}, users: []string{"alice"}, wantChanged: true},
		{name: "seeded entry kept", in: Inbound{Tag: "t", Protocol: "vless"},
			seed: map[string]state.XrayEntry{"t/alice": {ID: seededID}}, users: []string{"alice"}},
		{name: "no users", in: Inbound{Tag: "t", Protocol: "vless"}},
		{name: "entry of the wrong kind", in: Inbound{Tag: "t", Protocol: "hysteria"},
			seed:  map[string]state.XrayEntry{"t/alice": {ID: seededID}},
			users: []string{"bob", "alice"}, wantErr: "does not fit protocol hysteria"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newState()
			maps.Copy(st.Xray, tt.seed)
			before := maps.Clone(st.Xray)

			changed, err := enroll(st, tt.in, tt.users)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("enroll error = %v, want %q", err, tt.wantErr)
				}
				if strings.Contains(err.Error(), seededID.String()) {
					t.Errorf("error quotes the credential: %v", err)
				}
				if !maps.Equal(st.Xray, before) {
					t.Error("state changed on error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if changed != tt.wantChanged {
				t.Errorf("changed = %v, want %v", changed, tt.wantChanged)
			}
			for name, e := range tt.seed {
				if st.Xray[name] != e {
					t.Errorf("seeded entry %s changed", name)
				}
			}
			for _, user := range tt.users {
				e := st.Xray["t/"+user]
				if protocols[tt.in.Protocol].usesID {
					if e.ID == uuid.Nil || e.Password != "" {
						t.Errorf("%s: entry is not an id", user)
					}
					continue
				}
				raw, err := base64.RawURLEncoding.DecodeString(e.Password)
				if err != nil || len(raw) != passwordBytes || e.ID != uuid.Nil {
					t.Errorf("%s: entry is not a %d-byte base64url password", user, passwordBytes)
				}
			}
		})
	}
}
