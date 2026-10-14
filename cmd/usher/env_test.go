package main

import (
	"net/netip"
	"slices"
	"strings"
	"testing"
)

func TestEnvironmentFrom(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    environment
		wantErr string
	}{
		{
			name: "one DNS address",
			env:  map[string]string{"USHER_HOST": "203.0.113.10", "USHER_AWG_SOCKET": "/run/awg.sock", "USHER_DNS": "1.1.1.1"},
			want: environment{
				Host:      netip.MustParseAddr("203.0.113.10"),
				DNS:       []netip.Addr{netip.MustParseAddr("1.1.1.1")},
				AWGSocket: "/run/awg.sock",
			},
		},
		{
			name: "two DNS addresses",
			env:  map[string]string{"USHER_HOST": "203.0.113.10", "USHER_AWG_SOCKET": "/run/awg.sock", "USHER_DNS": "1.1.1.1, 1.0.0.1"},
			want: environment{
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
			name:    "no backend socket does not ask for DNS",
			env:     map[string]string{"USHER_HOST": "203.0.113.10"},
			wantErr: "USHER_AWG_SOCKET or USHER_XRAY_SOCKET is required",
		},
		{
			name: "xray only needs no DNS",
			env:  map[string]string{"USHER_HOST": "203.0.113.10", "USHER_XRAY_SOCKET": "/run/xray/api.sock", "USHER_XRAY_CONFIG": "/etc/xray/config.json"},
			want: environment{
				Host:       netip.MustParseAddr("203.0.113.10"),
				XraySocket: "/run/xray/api.sock",
				XrayConfig: "/etc/xray/config.json",
			},
		},
		{
			name: "both backends",
			env: map[string]string{
				"USHER_HOST": "203.0.113.10", "USHER_AWG_SOCKET": "/run/awg.sock", "USHER_DNS": "1.1.1.1",
				"USHER_XRAY_SOCKET": "/run/xray/api.sock", "USHER_XRAY_CONFIG": "/etc/xray/config.json",
			},
			want: environment{
				Host:       netip.MustParseAddr("203.0.113.10"),
				DNS:        []netip.Addr{netip.MustParseAddr("1.1.1.1")},
				AWGSocket:  "/run/awg.sock",
				XraySocket: "/run/xray/api.sock",
				XrayConfig: "/etc/xray/config.json",
			},
		},
		{
			name:    "xray socket without config",
			env:     map[string]string{"USHER_HOST": "203.0.113.10", "USHER_XRAY_SOCKET": "/run/xray/api.sock"},
			wantErr: "USHER_XRAY_CONFIG",
		},
		{
			name:    "relative xray socket",
			env:     map[string]string{"USHER_HOST": "203.0.113.10", "USHER_XRAY_SOCKET": "api.sock", "USHER_XRAY_CONFIG": "/etc/xray/config.json"},
			wantErr: "USHER_XRAY_SOCKET",
		},
		{
			name:    "relative xray config",
			env:     map[string]string{"USHER_HOST": "203.0.113.10", "USHER_XRAY_SOCKET": "/run/xray/api.sock", "USHER_XRAY_CONFIG": "config.json"},
			wantErr: "USHER_XRAY_CONFIG",
		},
		{
			name:    "xray config without any socket",
			env:     map[string]string{"USHER_HOST": "203.0.113.10", "USHER_XRAY_CONFIG": "/etc/xray/config.json"},
			wantErr: "USHER_XRAY_CONFIG is set without USHER_XRAY_SOCKET",
		},
		{
			name: "xray config without xray socket",
			env: map[string]string{"USHER_HOST": "203.0.113.10", "USHER_AWG_SOCKET": "/run/awg.sock",
				"USHER_DNS": "1.1.1.1", "USHER_XRAY_CONFIG": "/etc/xray/config.json"},
			wantErr: "USHER_XRAY_CONFIG is set without USHER_XRAY_SOCKET",
		},
		{
			name: "DNS without awg socket",
			env: map[string]string{"USHER_HOST": "203.0.113.10", "USHER_DNS": "1.1.1.1",
				"USHER_XRAY_SOCKET": "/run/xray/api.sock", "USHER_XRAY_CONFIG": "/etc/xray/config.json"},
			wantErr: "USHER_DNS is set without USHER_AWG_SOCKET",
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
			got, err := environmentFrom(func(key string) string { return tt.env[key] })
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to name %s", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Host != tt.want.Host || !slices.Equal(got.DNS, tt.want.DNS) || got.AWGSocket != tt.want.AWGSocket ||
				got.XraySocket != tt.want.XraySocket || got.XrayConfig != tt.want.XrayConfig {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestEnvironmentFromFixedPaths(t *testing.T) {
	env := map[string]string{"USHER_HOST": "203.0.113.10", "USHER_AWG_SOCKET": "/run/awg.sock", "USHER_DNS": "1.1.1.1"}
	got, err := environmentFrom(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	if got.ConfigPath != "/srv/usher/config/usher.yml" ||
		got.ClientsDir != "/srv/usher/clients" ||
		got.StatePath != "/srv/usher/state/users.json" {
		t.Errorf("paths = %q %q %q", got.ConfigPath, got.ClientsDir, got.StatePath)
	}
}
