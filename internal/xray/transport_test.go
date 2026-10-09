package xray

import (
	"strings"
	"testing"

	"github.com/mrcsin/usher/gen/xray/common/serial"
	"github.com/mrcsin/usher/gen/xray/transport/internet"
	"github.com/mrcsin/usher/gen/xray/transport/internet/tcp"
)

func tcpStream(settings *serial.TypedMessage) *internet.StreamConfig {
	return &internet.StreamConfig{
		ProtocolName:      "tcp",
		TransportSettings: []*internet.TransportConfig{{ProtocolName: "tcp", Settings: settings}},
	}
}

func TestDecodeTCP(t *testing.T) {
	noop := &serial.TypedMessage{Type: "xray.transport.internet.headers.noop.ConnectionConfig"}
	http := &serial.TypedMessage{Type: "xray.transport.internet.headers.http.Config"}
	tests := []struct {
		name    string
		stream  *internet.StreamConfig
		wantErr string
	}{
		{name: "no transport settings", stream: &internet.StreamConfig{ProtocolName: "tcp"}},
		{name: "settings without header", stream: tcpStream(toTypedMessage(&tcp.Config{}))},
		{name: "no-op header", stream: tcpStream(toTypedMessage(&tcp.Config{HeaderSettings: noop}))},
		{name: "settings of another transport", stream: &internet.StreamConfig{
			ProtocolName:      "tcp",
			TransportSettings: []*internet.TransportConfig{{ProtocolName: "websocket", Settings: &serial.TypedMessage{Type: "xray.other.Config"}}},
		}},
		{name: "http header", stream: tcpStream(toTypedMessage(&tcp.Config{HeaderSettings: http})), wantErr: "header"},
		{name: "foreign settings type", stream: tcpStream(&serial.TypedMessage{Type: "xray.other.Config"}), wantErr: "xray.other.Config"},
		{name: "garbled settings", stream: tcpStream(&serial.TypedMessage{Type: typeName(&tcp.Config{}), Value: []byte{0xff}}), wantErr: "decoding tcp settings"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeTCP(tt.stream)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Encode() != "type=tcp" {
				t.Errorf("params = %q, want type=tcp", got.Encode())
			}
		})
	}
}
