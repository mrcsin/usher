package xray

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/mrcsin/usher/gen/xray/transport/internet"
	"github.com/mrcsin/usher/gen/xray/transport/internet/tcp"
)

// noopHeaderPackage is the proto package of the raw TCP header "none".
const noopHeaderPackage = "xray.transport.internet.headers.noop."

// transports maps a stream's protocol name to its decoder. The decoder returns the link parameters
// of the transport.
var transports = map[string]func(stream *internet.StreamConfig) (url.Values, error){
	transportTCP: decodeTCP,
}

// decodeTCP accepts raw TCP without a header: no transport settings, settings without a header
// and settings with the no-op header.
func decodeTCP(stream *internet.StreamConfig) (url.Values, error) {
	for _, transport := range stream.GetTransportSettings() {
		if transport.GetProtocolName() != transportTCP || transport.GetSettings() == nil {
			continue
		}
		settings := transport.GetSettings()
		if settings.GetType() != typeName(&tcp.Config{}) {
			return nil, fmt.Errorf("tcp settings have type %q", settings.GetType())
		}
		var config tcp.Config
		if err := proto.Unmarshal(settings.GetValue(), &config); err != nil {
			return nil, fmt.Errorf("decoding tcp settings: %w", err)
		}
		header := config.GetHeaderSettings()
		if header != nil && !strings.HasPrefix(header.GetType(), noopHeaderPackage) {
			return nil, errors.New(`tcp header is not "none"`)
		}
	}
	return url.Values{"type": {transportTCP}}, nil
}
