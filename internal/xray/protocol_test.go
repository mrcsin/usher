package xray

import (
	"testing"

	"github.com/google/uuid"

	"github.com/mrcsin/usher/internal/state"
)

func TestProtocolCredential(t *testing.T) {
	id := uuid.New()
	tests := []struct {
		name     string
		in       inbound
		entry    state.XrayEntry
		want     string
		wrongFor string // protocol whose credential function must return "" for this account
	}{
		{"vless vision", inbound{Protocol: "vless", Network: "raw", Security: "reality"},
			state.XrayEntry{ID: id}, id.String() + " xtls-rprx-vision", "trojan"},
		{"vless ws", inbound{Protocol: "vless", Network: "ws", Security: "tls"},
			state.XrayEntry{ID: id}, id.String() + " ", "vmess"},
		{"vless raw without security", inbound{Protocol: "vless", Network: "raw", Security: "none"},
			state.XrayEntry{ID: id}, id.String() + " ", "hysteria"},
		{"vmess", inbound{Protocol: "vmess"}, state.XrayEntry{ID: id}, id.String(), "vless"},
		{"trojan", inbound{Protocol: "trojan"}, state.XrayEntry{Password: "pw"}, "pw", "hysteria"},
		{"hysteria", inbound{Protocol: "hysteria"}, state.XrayEntry{Password: "pw"}, "pw", "trojan"},
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
