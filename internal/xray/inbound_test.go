package xray

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc"

	"github.com/mrcsin/usher/gen/xray/app/proxyman"
	"github.com/mrcsin/usher/gen/xray/app/proxyman/command"
	"github.com/mrcsin/usher/gen/xray/common/net"
	"github.com/mrcsin/usher/gen/xray/common/serial"
	"github.com/mrcsin/usher/gen/xray/core"
	vlessinbound "github.com/mrcsin/usher/gen/xray/proxy/vless/inbound"
	"github.com/mrcsin/usher/gen/xray/transport/internet"
	"github.com/mrcsin/usher/gen/xray/transport/internet/reality"
)

// fakeHandlerService implements command.HandlerServiceClient with func fields.
type fakeHandlerService struct {
	command.HandlerServiceClient

	inbounds func() (*command.ListInboundsResponse, error)
}

func (f *fakeHandlerService) ListInbounds(context.Context, *command.ListInboundsRequest, ...grpc.CallOption) (*command.ListInboundsResponse, error) {
	return f.inbounds()
}

func portList(from, to uint32) *net.PortList {
	return &net.PortList{Range: []*net.PortRange{{From: from, To: to}}}
}

func realityStream(t *testing.T) *internet.StreamConfig {
	t.Helper()
	return &internet.StreamConfig{
		ProtocolName:     "tcp",
		SecurityType:     typeName(&reality.Config{}),
		SecuritySettings: realitySettings(t, validReality(t)),
	}
}

func inboundConfig(tag string, ports *net.PortList, stream *internet.StreamConfig) *core.InboundHandlerConfig {
	return &core.InboundHandlerConfig{
		Tag:              tag,
		ReceiverSettings: toTypedMessage(&proxyman.ReceiverConfig{PortList: ports, StreamSettings: stream}),
		ProxySettings:    toTypedMessage(&vlessinbound.Config{Decryption: "none"}),
	}
}

func listOf(configs ...*core.InboundHandlerConfig) command.HandlerServiceClient {
	return &fakeHandlerService{inbounds: func() (*command.ListInboundsResponse, error) {
		return &command.ListInboundsResponse{Inbounds: configs}, nil
	}}
}

func TestListInbounds(t *testing.T) {
	t.Run("valid inbound", func(t *testing.T) {
		client := listOf(inboundConfig("vless-reality", portList(443, 443), realityStream(t)))
		got, err := listInbounds(t.Context(), client)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].err != nil {
			t.Fatalf("inbounds = %+v", got)
		}
		in := got[0]
		if in.tag != "vless-reality" || in.port != 443 {
			t.Errorf("tag %q port %d", in.tag, in.port)
		}
		if in.params.Get("type") != "tcp" || in.params.Get("security") != "reality" || in.params.Get("pbk") == "" {
			t.Errorf("params = %v", in.params)
		}
		if in.protocol.account == nil {
			t.Error("protocol has no account builder")
		}
	})

	t.Run("non-vless inbound is skipped", func(t *testing.T) {
		api := &core.InboundHandlerConfig{
			Tag:           "api",
			ProxySettings: &serial.TypedMessage{Type: "xray.proxy.dokodemo.Config"},
		}
		got, err := listInbounds(t.Context(), listOf(api))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("inbounds = %+v, want none", got)
		}
	})

	t.Run("list error", func(t *testing.T) {
		client := &fakeHandlerService{inbounds: func() (*command.ListInboundsResponse, error) {
			return nil, errors.New("unavailable")
		}}
		if _, err := listInbounds(t.Context(), client); err == nil || !strings.Contains(err.Error(), "unavailable") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestListInboundsFailsUnsupportedShapes(t *testing.T) {
	noSecurity := realityStream(t)
	noSecurity.SecurityType = ""
	noSecurity.SecuritySettings = nil
	tls := realityStream(t)
	tls.SecurityType = "tls"
	websocket := realityStream(t)
	websocket.ProtocolName = "websocket"

	wrongReceiver := inboundConfig("a", portList(443, 443), realityStream(t))
	wrongReceiver.ReceiverSettings = toTypedMessage(&vlessinbound.Config{})
	garbledReceiver := inboundConfig("a", portList(443, 443), realityStream(t))
	garbledReceiver.ReceiverSettings = &serial.TypedMessage{Type: typeName(&proxyman.ReceiverConfig{}), Value: []byte{0xff}}
	garbledProxy := inboundConfig("a", portList(443, 443), realityStream(t))
	garbledProxy.ProxySettings = &serial.TypedMessage{Type: typeName(&vlessinbound.Config{}), Value: []byte{0xff}}

	tests := []struct {
		name    string
		config  *core.InboundHandlerConfig
		wantErr string
	}{
		{name: "receiver of another type", config: wrongReceiver, wantErr: "receiver settings have type"},
		{name: "undecodable receiver", config: garbledReceiver, wantErr: "decoding receiver settings"},
		{name: "undecodable vless settings", config: garbledProxy, wantErr: "decoding vless settings"},
		{name: "port range", config: inboundConfig("a", portList(443, 450), realityStream(t)), wantErr: "port"},
		{name: "no port", config: inboundConfig("a", nil, realityStream(t)), wantErr: "port"},
		{name: "tls security", config: inboundConfig("a", portList(443, 443), tls), wantErr: `security "tls"`},
		{name: "empty security is named none", config: inboundConfig("a", portList(443, 443), noSecurity), wantErr: `security "none"`},
		{name: "websocket transport", config: inboundConfig("a", portList(443, 443), websocket), wantErr: `transport "websocket"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := listInbounds(t.Context(), listOf(tt.config))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].tag != "a" || got[0].err == nil || !strings.Contains(got[0].err.Error(), tt.wantErr) {
				t.Fatalf("inbounds = %+v, want one failed inbound containing %q", got, tt.wantErr)
			}
		})
	}
}

func TestInboundErrorsNeverContainPrivateKey(t *testing.T) {
	private, err := hex.DecodeString(privateKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	encodings := []string{
		string(private),
		privateKeyHex,
		strings.ToUpper(privateKeyHex),
		base64.StdEncoding.EncodeToString(private),
		base64.RawStdEncoding.EncodeToString(private),
		base64.URLEncoding.EncodeToString(private),
		base64.RawURLEncoding.EncodeToString(private),
	}

	bad := validReality(t)
	bad.ServerNames = nil
	shortKey := validReality(t)
	shortKey.PrivateKey = append(bytes.Clone(shortKey.PrivateKey), 0)
	garbled := realityStream(t)
	garbled.SecuritySettings[0].Value = append(garbled.SecuritySettings[0].Value, 0xff)
	tls := realityStream(t)
	tls.SecurityType = "tls"

	streams := map[string]*internet.StreamConfig{
		"no server names": {ProtocolName: "tcp", SecurityType: typeName(&reality.Config{}), SecuritySettings: realitySettings(t, bad)},
		"long key":        {ProtocolName: "tcp", SecurityType: typeName(&reality.Config{}), SecuritySettings: realitySettings(t, shortKey)},
		"garbled":         garbled,
		"tls with key":    tls,
	}
	for name, stream := range streams {
		t.Run(name, func(t *testing.T) {
			got, err := listInbounds(t.Context(), listOf(inboundConfig("a", portList(443, 443), stream)))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].err == nil {
				t.Fatalf("inbounds = %+v, want one failed inbound", got)
			}
			for _, encoding := range encodings {
				if strings.Contains(got[0].err.Error(), encoding) {
					t.Errorf("error %q contains the private key as %q", got[0].err, encoding)
				}
			}
		})
	}
}
