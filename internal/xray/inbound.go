package xray

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"

	"google.golang.org/protobuf/proto"

	"github.com/mrcsin/usher/gen/xray/app/proxyman"
	"github.com/mrcsin/usher/gen/xray/app/proxyman/command"
	"github.com/mrcsin/usher/gen/xray/core"
)

// inbound is a VLESS inbound as the running Xray holds it. A failed inbound has err set and no
// other field but tag.
type inbound struct {
	tag      string
	port     uint16
	protocol protocol
	// params are the link parameters of the transport and the security.
	params url.Values
	err    error
}

// listInbounds returns every tagged inbound whose proxy settings type has a protocol entry. Other
// inbounds, such as the api tunnel, and inbounds without a tag are not usher interfaces and are
// left out.
func listInbounds(ctx context.Context, client command.HandlerServiceClient) ([]inbound, error) {
	response, err := client.ListInbounds(ctx, &command.ListInboundsRequest{})
	if err != nil {
		return nil, fmt.Errorf("listing inbounds: %w", err)
	}
	var inbounds []inbound
	for _, config := range response.GetInbounds() {
		decodeProtocol, ok := protocols[config.GetProxySettings().GetType()]
		if !ok {
			continue
		}
		if config.GetTag() == "" {
			continue
		}
		in, err := decodeInbound(config, decodeProtocol)
		if err != nil {
			in = inbound{tag: config.GetTag(), err: err}
		}
		inbounds = append(inbounds, in)
	}
	return inbounds, nil
}

func decodeInbound(config *core.InboundHandlerConfig, decodeProtocol protocolDecoder) (inbound, error) {
	receiverSettings := config.GetReceiverSettings()
	if receiverSettings.GetType() != typeName(&proxyman.ReceiverConfig{}) {
		return inbound{}, fmt.Errorf("receiver settings have type %q", receiverSettings.GetType())
	}
	var receiver proxyman.ReceiverConfig
	if err := proto.Unmarshal(receiverSettings.GetValue(), &receiver); err != nil {
		return inbound{}, fmt.Errorf("decoding receiver settings: %w", err)
	}
	port, err := singlePort(&receiver)
	if err != nil {
		return inbound{}, err
	}
	stream := receiver.GetStreamSettings()
	transportName := stream.GetProtocolName()
	decodeTransport, ok := transports[transportName]
	if !ok {
		return inbound{}, fmt.Errorf("transport %q is not supported", transportName)
	}
	params, err := decodeTransport(stream)
	if err != nil {
		return inbound{}, err
	}
	if len(stream.GetTcpmasks())+len(stream.GetUdpmasks()) > 0 {
		return inbound{}, errors.New("finalmask is not supported")
	}
	securityName := stream.GetSecurityType()
	decodeSecurity, ok := securities[securityName]
	if !ok {
		if securityName == "" {
			securityName = securityNone
		}
		return inbound{}, fmt.Errorf("security %q is not supported", securityName)
	}
	securityParams, err := decodeSecurity(stream.GetSecuritySettings())
	if err != nil {
		return inbound{}, err
	}
	for key, values := range securityParams {
		params[key] = values
	}
	decoded, err := decodeProtocol(config.GetProxySettings().GetValue(), transportName, params.Get("security"))
	if err != nil {
		return inbound{}, err
	}
	return inbound{tag: config.GetTag(), port: port, protocol: decoded, params: params}, nil
}

func singlePort(receiver *proxyman.ReceiverConfig) (uint16, error) {
	ranges := receiver.GetPortList().GetRange()
	if len(ranges) != 1 || ranges[0].GetFrom() != ranges[0].GetTo() || ranges[0].GetFrom() == 0 || ranges[0].GetFrom() > math.MaxUint16 {
		return 0, errors.New("port must be a single number")
	}
	return uint16(ranges[0].GetFrom()), nil
}
