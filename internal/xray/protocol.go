package xray

import (
	"github.com/xtls/xray-core/proxy/hysteria/account"
	"github.com/xtls/xray-core/proxy/trojan"
	"github.com/xtls/xray-core/proxy/vless"
	"github.com/xtls/xray-core/proxy/vmess"
	"google.golang.org/protobuf/proto"

	"github.com/mrcsin/usher/internal/state"
)

const vlessVisionFlow = "xtls-rprx-vision"

type protocolSpec struct {
	proxyType      string
	usesID         bool
	clientSettings func(in inbound, e state.XrayEntry, settings map[string]any)
	account        func(in inbound, e state.XrayEntry) proto.Message
	// credential is "" for an account of another type; equal credentials are the same user.
	credential func(account proto.Message) string
}

var protocols = map[string]protocolSpec{
	protocolVLESS: {
		proxyType: "xray.proxy.vless.inbound.Config",
		usesID:    true,
		clientSettings: func(in inbound, e state.XrayEntry, settings map[string]any) {
			settings["id"], settings["encryption"], settings["flow"] = e.ID.String(), "none", vlessFlow(in)
		},
		account: func(in inbound, e state.XrayEntry) proto.Message {
			return &vless.Account{Id: e.ID.String(), Flow: vlessFlow(in), Encryption: "none"}
		},
		credential: func(a proto.Message) string {
			if v, ok := a.(*vless.Account); ok {
				return v.GetId() + " " + v.GetFlow()
			}
			return ""
		},
	},
	protocolVMess: {
		proxyType: "xray.proxy.vmess.inbound.Config",
		usesID:    true,
		clientSettings: func(_ inbound, e state.XrayEntry, settings map[string]any) {
			settings["id"], settings["security"] = e.ID.String(), "auto"
		},
		account: func(_ inbound, e state.XrayEntry) proto.Message {
			return &vmess.Account{Id: e.ID.String()}
		},
		// Xray adds SecuritySettings to a stored VMess account, so only the id is compared.
		credential: func(a proto.Message) string {
			if v, ok := a.(*vmess.Account); ok {
				return v.GetId()
			}
			return ""
		},
	},
	protocolTrojan: {
		proxyType: "xray.proxy.trojan.ServerConfig",
		clientSettings: func(_ inbound, e state.XrayEntry, settings map[string]any) {
			settings["password"] = e.Password
		},
		account: func(_ inbound, e state.XrayEntry) proto.Message {
			return &trojan.Account{Password: e.Password}
		},
		credential: func(a proto.Message) string {
			if v, ok := a.(*trojan.Account); ok {
				return v.GetPassword()
			}
			return ""
		},
	},
	protocolHysteria: {
		proxyType: "xray.proxy.hysteria.ServerConfig",
		clientSettings: func(_ inbound, _ state.XrayEntry, settings map[string]any) {
			settings["version"] = 2
		},
		account: func(_ inbound, e state.XrayEntry) proto.Message {
			return &account.Account{Auth: e.Password}
		},
		credential: func(a proto.Message) string {
			if v, ok := a.(*account.Account); ok {
				return v.GetAuth()
			}
			return ""
		},
	},
}

func vlessFlow(in inbound) string {
	if in.Network == "raw" && (in.Security == securityTLS || in.Security == securityReality) {
		return vlessVisionFlow
	}
	return ""
}
