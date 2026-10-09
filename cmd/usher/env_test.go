package main

import (
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/mrcsin/usher/internal/pass"
)

func TestSettingsFrom(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    pass.Settings
		wantErr string
	}{
		{
			name: "one DNS address",
			env:  map[string]string{"USHER_HOST": "203.0.113.10", "USHER_AWG_SOCKET": "/run/awg.sock", "USHER_DNS": "1.1.1.1"},
			want: pass.Settings{
				Host:      netip.MustParseAddr("203.0.113.10"),
				DNS:       []netip.Addr{netip.MustParseAddr("1.1.1.1")},
				AWGSocket: "/run/awg.sock",
			},
		},
		{
			name: "two DNS addresses",
			env:  map[string]string{"USHER_HOST": "203.0.113.10", "USHER_AWG_SOCKET": "/run/awg.sock", "USHER_DNS": "1.1.1.1, 1.0.0.1"},
			want: pass.Settings{
				Host:      netip.MustParseAddr("203.0.113.10"),
				DNS:       []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("1.0.0.1")},
				AWGSocket: "/run/awg.sock",
			},
		},
		{
			name:    "missing host",
			env:     map[string]string{"USHER_AWG_SOCKET": "/run/awg.sock", "USHER_DNS": "1.1.1.1"},
			wantErr: "USHER_HOST",
		},
		{
			name:    "host name",
			env:     map[string]string{"USHER_HOST": "vpn.example.com", "USHER_AWG_SOCKET": "/run/awg.sock", "USHER_DNS": "1.1.1.1"},
			wantErr: "USHER_HOST",
		},
		{
			name:    "IPv6 host",
			env:     map[string]string{"USHER_HOST": "2001:db8::1", "USHER_AWG_SOCKET": "/run/awg.sock", "USHER_DNS": "1.1.1.1"},
			wantErr: "USHER_HOST",
		},
		{
			name:    "host with port",
			env:     map[string]string{"USHER_HOST": "203.0.113.10:51820", "USHER_AWG_SOCKET": "/run/awg.sock", "USHER_DNS": "1.1.1.1"},
			wantErr: "USHER_HOST",
		},
		{
			name:    "missing awg socket",
			env:     map[string]string{"USHER_HOST": "203.0.113.10", "USHER_DNS": "1.1.1.1"},
			wantErr: "USHER_AWG_SOCKET",
		},
		{
			name:    "relative awg socket",
			env:     map[string]string{"USHER_HOST": "203.0.113.10", "USHER_AWG_SOCKET": "awg.sock", "USHER_DNS": "1.1.1.1"},
			wantErr: "USHER_AWG_SOCKET",
		},
		{
			name:    "empty DNS",
			env:     map[string]string{"USHER_HOST": "203.0.113.10", "USHER_AWG_SOCKET": "/run/awg.sock", "USHER_DNS": ""},
			wantErr: "USHER_DNS",
		},
		{
			name:    "IPv6 DNS",
			env:     map[string]string{"USHER_HOST": "203.0.113.10", "USHER_AWG_SOCKET": "/run/awg.sock", "USHER_DNS": "2606:4700:4700::1111"},
			wantErr: "USHER_DNS",
		},
		{
			name:    "DNS with port",
			env:     map[string]string{"USHER_HOST": "203.0.113.10", "USHER_AWG_SOCKET": "/run/awg.sock", "USHER_DNS": "1.1.1.1:53"},
			wantErr: "USHER_DNS",
		},
		{
			name:    "empty DNS element",
			env:     map[string]string{"USHER_HOST": "203.0.113.10", "USHER_AWG_SOCKET": "/run/awg.sock", "USHER_DNS": "1.1.1.1,"},
			wantErr: "USHER_DNS",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := settingsFrom(func(key string) string { return tt.env[key] })
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to name %s", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Host != tt.want.Host || !slices.Equal(got.DNS, tt.want.DNS) || got.AWGSocket != tt.want.AWGSocket {
				t.Errorf("host/dns/socket = %v %v %q, want %v %v %q",
					got.Host, got.DNS, got.AWGSocket, tt.want.Host, tt.want.DNS, tt.want.AWGSocket)
			}
		})
	}
}

func TestSettingsFromFixedPaths(t *testing.T) {
	env := map[string]string{"USHER_HOST": "203.0.113.10", "USHER_AWG_SOCKET": "/run/awg.sock", "USHER_DNS": "1.1.1.1"}
	got, err := settingsFrom(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	if got.ConfigPath != "/srv/usher/config/usher.yml" ||
		got.ClientsDir != "/srv/usher/clients" ||
		got.StatePath != "/srv/usher/state/users.json" {
		t.Errorf("paths = %q %q %q", got.ConfigPath, got.ClientsDir, got.StatePath)
	}
}
