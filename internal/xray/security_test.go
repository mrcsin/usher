package xray

import (
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"strings"
	"testing"

	"github.com/mrcsin/usher/gen/xray/common/serial"
	"github.com/mrcsin/usher/gen/xray/transport/internet/reality"
)

// RFC 7748 section 6.1, Alice: a private key and its public key.
const (
	privateKeyHex = "77076d0a7318a57d3c16c17251b26645df4c2f87ebc0992ab177fba51db92c2a"
	publicKeyHex  = "8520f0098930a754748b7ddcb43ef75a0dbf3a0d26381af4eba4a98eaa9b4e6a"
)

func realitySettings(t *testing.T, config *reality.Config) []*serial.TypedMessage {
	t.Helper()
	return []*serial.TypedMessage{toTypedMessage(config)}
}

func validReality(t *testing.T) *reality.Config {
	t.Helper()
	private, err := hex.DecodeString(privateKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	return &reality.Config{
		PrivateKey:  private,
		ServerNames: []string{"example.com"},
		ShortIds:    [][]byte{{0xab, 0, 0, 0, 0, 0, 0, 0}},
	}
}

func TestDecodeReality(t *testing.T) {
	public, err := hex.DecodeString(publicKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	wantPBK := base64.RawURLEncoding.EncodeToString(public)

	tests := []struct {
		name    string
		change  func(*reality.Config)
		want    url.Values
		wantErr string
	}{
		{
			name:   "trimmed short id",
			change: func(*reality.Config) {},
			want:   url.Values{"security": {"reality"}, "sni": {"example.com"}, "pbk": {wantPBK}, "sid": {"ab"}},
		},
		{
			name:   "all-zero short id is empty",
			change: func(c *reality.Config) { c.ShortIds = [][]byte{make([]byte, 8)} },
			want:   url.Values{"security": {"reality"}, "sni": {"example.com"}, "pbk": {wantPBK}, "sid": {""}},
		},
		{
			name:   "first short id wins",
			change: func(c *reality.Config) { c.ShortIds = [][]byte{{0x01, 0x02, 0, 0, 0, 0, 0, 0}, {0xff}} },
			want:   url.Values{"security": {"reality"}, "sni": {"example.com"}, "pbk": {wantPBK}, "sid": {"0102"}},
		},
		{
			name:   "first non-empty server name",
			change: func(c *reality.Config) { c.ServerNames = []string{"", "second.example"} },
			want:   url.Values{"security": {"reality"}, "sni": {"second.example"}, "pbk": {wantPBK}, "sid": {"ab"}},
		},
		{
			name:    "no usable server name",
			change:  func(c *reality.Config) { c.ServerNames = []string{"", ""} },
			wantErr: "server_names",
		},
		{
			name:    "no short ids",
			change:  func(c *reality.Config) { c.ShortIds = nil },
			wantErr: "short_ids",
		},
		{
			name:    "short private key",
			change:  func(c *reality.Config) { c.PrivateKey = c.PrivateKey[:31] },
			wantErr: "private_key",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := validReality(t)
			tt.change(config)
			got, err := decodeReality(realitySettings(t, config))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Encode() != tt.want.Encode() {
				t.Errorf("params = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDecodeRealityMissingSettings(t *testing.T) {
	other := []*serial.TypedMessage{{Type: "xray.transport.internet.tls.Config"}}
	for name, settings := range map[string][]*serial.TypedMessage{"none": nil, "other type": other} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeReality(settings); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}
