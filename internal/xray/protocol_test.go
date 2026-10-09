package xray

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	vlessaccount "github.com/mrcsin/usher/gen/xray/proxy/vless"
	vlessinbound "github.com/mrcsin/usher/gen/xray/proxy/vless/inbound"
	"github.com/mrcsin/usher/internal/state"
)

func vlessSettings(t *testing.T, decryption string) []byte {
	t.Helper()
	value, err := proto.Marshal(&vlessinbound.Config{Decryption: decryption})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestDecodeVLESS(t *testing.T) {
	tests := []struct {
		name       string
		decryption string
		transport  string
		security   string
		wantErr    string
	}{
		{name: "tcp and reality", decryption: "none", transport: "tcp", security: "reality"},
		{name: "other decryption", decryption: "mlkem768x25519plus.native.0rtt", transport: "tcp", security: "reality", wantErr: "decryption"},
		{name: "empty decryption", decryption: "", transport: "tcp", security: "reality", wantErr: "decryption"},
		{name: "other transport", decryption: "none", transport: "ws", security: "reality", wantErr: `"ws"`},
		{name: "other security", decryption: "none", transport: "tcp", security: "tls", wantErr: `"tls"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeVLESS(vlessSettings(t, tt.decryption), tt.transport, tt.security)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestVLESSAccountCarriesIDAndVisionFlow(t *testing.T) {
	p, err := decodeVLESS(vlessSettings(t, "none"), "tcp", "reality")
	if err != nil {
		t.Fatal(err)
	}
	entry := state.XrayEntry{ID: [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}}
	message := p.account(entry)
	if message.GetType() != "xray.proxy.vless.Account" {
		t.Fatalf("type = %q", message.GetType())
	}
	var account vlessaccount.Account
	if err := proto.Unmarshal(message.GetValue(), &account); err != nil {
		t.Fatal(err)
	}
	if account.GetId() != entry.ID.String() || account.GetFlow() != "xtls-rprx-vision" {
		t.Errorf("account = id %q flow %q", account.GetId(), account.GetFlow())
	}
}
