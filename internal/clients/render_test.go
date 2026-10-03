package clients

import (
	"bytes"
	"net/netip"
	"os"
	"strings"
	"testing"

	awgv1 "github.com/mrcsin/awg-grpc/gen/awg/v1"

	"github.com/mrcsin/usher/internal/state"
)

func testEntry() state.Entry {
	return state.Entry{
		Address:      netip.MustParseAddr("10.8.1.2"),
		PrivateKey:   state.Key(bytes.Repeat([]byte{1}, state.KeyLength)),
		PresharedKey: state.Key(bytes.Repeat([]byte{2}, state.KeyLength)),
	}
}

func testInterface() *awgv1.InterfaceStatus {
	return &awgv1.InterfaceStatus{
		Name:       "awg0",
		PublicKey:  []byte("server-public-key-fixture-32byte"),
		ListenPort: 51820,
		ClientParams: []*awgv1.ConfigParam{
			{Key: "Jc", Value: "4"},
			{Key: "Jmin", Value: "10"},
			{Key: "S1", Value: "20"},
			{Key: "H1", Value: "100000-100999"},
			{Key: "HeaderProtectionKey", Value: "aGVhZGVyLXByb3RlY3Rpb24tZml4dHVyZS0zMi1ieXQ="},
		},
	}
}

func TestRenderGolden(t *testing.T) {
	got, err := Render(testEntry(), testInterface(), netip.MustParseAddr("203.0.113.10"),
		[]netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("1.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/phone.conf")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("rendered config differs from testdata/phone.conf:\n%s", got)
	}
}

func TestRender(t *testing.T) {
	host := netip.MustParseAddr("198.51.100.7")
	oneDNS := []netip.Addr{netip.MustParseAddr("9.9.9.9")}
	twoDNS := []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("8.8.8.8")}

	tests := []struct {
		name              string
		mutate            func(*awgv1.InterfaceStatus)
		dns               []netip.Addr
		want              []string
		wantErr           bool
		errMustNotContain []string
	}{
		{
			name: "endpoint is address and port",
			dns:  oneDNS,
			want: []string{"Endpoint = 198.51.100.7:51820\n"},
		},
		{
			name: "two dns addresses are joined by comma and space",
			dns:  twoDNS,
			want: []string{"DNS = 1.1.1.1, 8.8.8.8\n"},
		},
		{
			name: "client params keep their order",
			dns:  oneDNS,
			want: []string{"Jc = 4\nJmin = 10\nS1 = 20\nH1 = 100000-100999\nHeaderProtectionKey = "},
		},
		{
			name:   "no client params",
			dns:    oneDNS,
			mutate: func(i *awgv1.InterfaceStatus) { i.ClientParams = nil },
			want:   []string{"DNS = 9.9.9.9\n\n[Peer]\n"},
		},
		{
			name: "newline in a value is an error",
			dns:  oneDNS,
			mutate: func(i *awgv1.InterfaceStatus) {
				i.ClientParams[0].Value = "4\n[Peer]"
			},
			wantErr:           true,
			errMustNotContain: []string{"4\n[Peer]", "[Peer]"},
		},
		{
			name: "carriage return in a value is an error",
			dns:  oneDNS,
			mutate: func(i *awgv1.InterfaceStatus) {
				i.ClientParams[1].Value = "10\r"
			},
			wantErr:           true,
			errMustNotContain: []string{"10\r"},
		},
		{
			name:    "listen port above 65535 is an error",
			dns:     oneDNS,
			mutate:  func(i *awgv1.InterfaceStatus) { i.ListenPort = 65536 },
			wantErr: true,
		},
		{
			name: "newline in a key is an error",
			dns:  oneDNS,
			mutate: func(i *awgv1.InterfaceStatus) {
				i.ClientParams[0].Key = "Jc\nX"
			},
			wantErr:           true,
			errMustNotContain: []string{"Jc\nX"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			iface := testInterface()
			if tt.mutate != nil {
				tt.mutate(iface)
			}
			got, err := Render(testEntry(), iface, host, tt.dns)
			if tt.wantErr {
				if err == nil {
					t.Fatal("want an error")
				}
				for _, leak := range append(tt.errMustNotContain, "aGVhZGVy", testEntry().PrivateKey.String(), testEntry().PresharedKey.String()) {
					if strings.Contains(err.Error(), leak) {
						t.Errorf("error quotes %q: %v", leak, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range tt.want {
				if !strings.Contains(string(got), w) {
					t.Errorf("config lacks %q:\n%s", w, got)
				}
			}
		})
	}
}
