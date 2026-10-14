package xray

import (
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/proxy/hysteria/account"
	"github.com/xtls/xray-core/proxy/trojan"
	"github.com/xtls/xray-core/proxy/vless"
	"github.com/xtls/xray-core/proxy/vmess"
	"google.golang.org/protobuf/proto"

	"github.com/mrcsin/usher/internal/state"
)

// vlessVisionFlow is the VLESS flow of a raw inbound behind TLS or Reality.
const vlessVisionFlow = "xtls-rprx-vision"

// protocolSpec describes how one inbound protocol holds its users.
type protocolSpec struct {
	// usesID is true when the credential is a UUID in XrayEntry.ID, false when it is a password in
	// XrayEntry.Password.
	usesID bool
	// account builds the account Xray stores for the entry on the inbound.
	account func(in Inbound, e state.XrayEntry) proto.Message
	// credential returns the part of an account that identifies what the client must present, or
	// "" for an account of another type. Two accounts with the same credential are the same user.
	credential func(account proto.Message) string
}

// protocols maps the lower case protocol of an inbound to its spec.
var protocols = map[string]protocolSpec{
	"vless": {
		usesID: true,
		account: func(in Inbound, e state.XrayEntry) proto.Message {
			return &vless.Account{Id: e.ID.String(), Flow: vlessFlow(in), Encryption: "none"}
		},
		credential: func(a proto.Message) string {
			if v, ok := a.(*vless.Account); ok {
				return v.GetId() + " " + v.GetFlow()
			}
			return ""
		},
	},
	"vmess": {
		usesID: true,
		account: func(_ Inbound, e state.XrayEntry) proto.Message {
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
	"trojan": {
		account: func(_ Inbound, e state.XrayEntry) proto.Message {
			return &trojan.Account{Password: e.Password}
		},
		credential: func(a proto.Message) string {
			if v, ok := a.(*trojan.Account); ok {
				return v.GetPassword()
			}
			return ""
		},
	},
	"hysteria": {
		account: func(_ Inbound, e state.XrayEntry) proto.Message {
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

// vlessFlow returns the flow of the VLESS users of the inbound: vision for raw behind TLS or
// Reality, none otherwise.
func vlessFlow(in Inbound) string {
	if in.Network == "raw" && (in.Security == "tls" || in.Security == "reality") {
		return vlessVisionFlow
	}
	return ""
}

// typedAccount wraps the account of the entry for an AddUserOperation.
func typedAccount(in Inbound, e state.XrayEntry) *serial.TypedMessage {
	return serial.ToTypedMessage(protocols[in.Protocol].account(in, e))
}
