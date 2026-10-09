// Package xray is the Xray backend: it reads the VLESS inbounds of a running Xray through its
// gRPC API and moves usher's users into them.
package xray

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"

	"google.golang.org/protobuf/proto"

	"github.com/mrcsin/usher/gen/xray/common/serial"
	vlessaccount "github.com/mrcsin/usher/gen/xray/proxy/vless"
	vlessinbound "github.com/mrcsin/usher/gen/xray/proxy/vless/inbound"
	"github.com/mrcsin/usher/internal/state"
)

const (
	// flowVision is the flow of VLESS with Reality over raw TCP.
	flowVision = "xtls-rprx-vision"

	decryptionNone = "none"
)

// protocolDecoder builds the protocol of an inbound from its proxy settings and the names of its
// transport and security.
type protocolDecoder func(settings []byte, transport, security string) (protocol, error)

// protocol is what a proxy-settings entry builds for an inbound.
type protocol struct {
	// account returns the account Xray holds for an entry.
	account func(e state.XrayEntry) *serial.TypedMessage
	// link returns the client link of a user. params are the inbound's transport and security
	// parameters.
	link func(e state.XrayEntry, user string, host netip.Addr, port uint16, params url.Values) string
}

// protocols maps the type of an inbound's proxy settings to its decoder.
var protocols = map[string]protocolDecoder{
	typeName(&vlessinbound.Config{}): decodeVLESS,
}

func typeName(m proto.Message) string {
	return string(m.ProtoReflect().Descriptor().FullName())
}

func toTypedMessage(m proto.Message) *serial.TypedMessage {
	// Marshal fails on a proto3 string field with invalid UTF-8. The strings here are a tag Xray
	// decoded and a user name that config validation accepted, so the error cannot happen.
	value, _ := proto.Marshal(m)
	return &serial.TypedMessage{Type: typeName(m), Value: value}
}

func decodeVLESS(settings []byte, transport, security string) (protocol, error) {
	var config vlessinbound.Config
	if err := proto.Unmarshal(settings, &config); err != nil {
		return protocol{}, fmt.Errorf("decoding vless settings: %w", err)
	}
	if len(config.GetUsers()) > 0 {
		return protocol{}, errors.New("vless clients must be empty: usher owns the users")
	}
	if config.GetDecryption() != decryptionNone {
		return protocol{}, errors.New(`vless decryption is not "none"`)
	}
	if transport != transportTCP || security != securityReality {
		return protocol{}, fmt.Errorf("vless over transport %q with security %q is not supported",
			transport, security)
	}
	return protocol{
		account: func(e state.XrayEntry) *serial.TypedMessage {
			return toTypedMessage(&vlessaccount.Account{Id: e.ID.String(), Flow: flowVision})
		},
		link: func(e state.XrayEntry, user string, host netip.Addr, port uint16, params url.Values) string {
			return vlessLink(e, user, flowVision, host, port, params)
		},
	}, nil
}
