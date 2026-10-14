package xray

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/xtls/xray-core/app/proxyman"
	"github.com/xtls/xray-core/app/proxyman/command"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/tls"
	"google.golang.org/protobuf/proto"
)

// managedProxyTypes are the proxy settings types of the inbounds the backend manages.
var managedProxyTypes = []string{
	"xray.proxy.vless.inbound.Config",
	"xray.proxy.hysteria.ServerConfig",
	"xray.proxy.vmess.inbound.Config",
	"xray.proxy.trojan.ServerConfig",
}

// checkDrift compares every valid inbound of the file with the inbound of the same tag in the
// running Xray. The result maps a tag to the reason its running inbound differs from the file or
// is missing; a tag without an entry matches. A mismatch means Xray runs an older file, so the
// client files for the tag are not trustworthy until Xray restarts.
//
// Certificate entries that name a file are left out on both sides: usher cannot read the key
// files, and Xray reloads those files itself. A reason names the differing parts and never a
// value. Running inbounds of a managed protocol that the file does not define are logged.
func checkDrift(ctx context.Context, client command.HandlerServiceClient, log *slog.Logger,
	inbounds []Inbound) (map[string]error, error) {
	resp, err := client.ListInbounds(ctx, &command.ListInboundsRequest{})
	if err != nil {
		return nil, fmt.Errorf("listing xray inbounds: %w", err)
	}
	running := make(map[string]*core.InboundHandlerConfig, len(resp.GetInbounds()))
	for _, handler := range resp.GetInbounds() {
		running[handler.GetTag()] = handler
	}

	drift := map[string]error{}
	inFile := make(map[string]bool, len(inbounds))
	for _, in := range inbounds {
		inFile[in.Tag] = true
		if in.Err != nil {
			continue
		}
		live, ok := running[in.Tag]
		if !ok {
			drift[in.Tag] = fmt.Errorf("inbound %q: not running in Xray, restart Xray to load the file", in.Tag)
			continue
		}
		parts, err := differingParts(in, live)
		switch {
		case err != nil:
			drift[in.Tag] = fmt.Errorf("inbound %q: comparing with the running Xray: %w", in.Tag, err)
		case len(parts) > 0:
			drift[in.Tag] = fmt.Errorf("inbound %q: differs from the running Xray in %s, restart Xray to load the file",
				in.Tag, strings.Join(parts, ", "))
		}
	}

	for tag, handler := range running {
		if !inFile[tag] && slices.Contains(managedProxyTypes, handler.GetProxySettings().GetType()) {
			log.Warn("xray runs an inbound that the config file does not define", "inbound", tag)
		}
	}
	return drift, nil
}

// differingParts returns the names of the parts of the file inbound that differ from the running
// one: "proxy type", "proxy settings", "port", "stream settings" and "receiver settings".
func differingParts(in Inbound, live *core.InboundHandlerConfig) ([]string, error) {
	raw, err := withoutFileCertificates(in.Raw)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("encoding the file inbound: %w", err)
	}
	var detour conf.InboundDetourConfig
	if err := json.Unmarshal(encoded, &detour); err != nil {
		return nil, fmt.Errorf("decoding the file inbound: %w", err)
	}
	built, err := detour.Build()
	if err != nil {
		return nil, fmt.Errorf("building the file inbound: %w", err)
	}

	var parts []string
	if built.GetProxySettings().GetType() != live.GetProxySettings().GetType() {
		parts = append(parts, "proxy type")
	} else if equal, err := equalMessages(built.GetProxySettings(), live.GetProxySettings()); err != nil {
		return nil, err
	} else if !equal {
		parts = append(parts, "proxy settings")
	}

	fileReceiver, err := receiver(built)
	if err != nil {
		return nil, err
	}
	liveReceiver, err := receiver(live)
	if err != nil {
		return nil, err
	}
	for _, r := range []*proxyman.ReceiverConfig{fileReceiver, liveReceiver} {
		if err := dropPathCertificates(r.GetStreamSettings()); err != nil {
			return nil, err
		}
	}
	if !proto.Equal(fileReceiver.GetPortList(), liveReceiver.GetPortList()) {
		parts = append(parts, "port")
	}
	if !proto.Equal(fileReceiver.GetStreamSettings(), liveReceiver.GetStreamSettings()) {
		parts = append(parts, "stream settings")
	}
	for _, r := range []*proxyman.ReceiverConfig{fileReceiver, liveReceiver} {
		r.PortList = nil
		r.StreamSettings = nil
	}
	if !proto.Equal(fileReceiver, liveReceiver) {
		parts = append(parts, "receiver settings")
	}
	return parts, nil
}

func equalMessages(a, b *serial.TypedMessage) (bool, error) {
	left, err := a.GetInstance()
	if err != nil {
		return false, fmt.Errorf("decoding proxy settings: %w", err)
	}
	right, err := b.GetInstance()
	if err != nil {
		return false, fmt.Errorf("decoding proxy settings: %w", err)
	}
	return proto.Equal(left, right), nil
}

// receiver unpacks the receiver settings into a fresh message the caller may modify.
func receiver(handler *core.InboundHandlerConfig) (*proxyman.ReceiverConfig, error) {
	instance, err := handler.GetReceiverSettings().GetInstance()
	if err != nil {
		return nil, fmt.Errorf("decoding receiver settings: %w", err)
	}
	r, ok := instance.(*proxyman.ReceiverConfig)
	if !ok {
		return nil, errors.New("receiver settings are not a receiver config")
	}
	return r, nil
}

var tlsConfigType = string((&tls.Config{}).ProtoReflect().Descriptor().FullName())

// dropPathCertificates removes the certificates that Xray loads from a file from the TLS settings
// of stream.
func dropPathCertificates(stream *internet.StreamConfig) error {
	for i, settings := range stream.GetSecuritySettings() {
		if settings.GetType() != tlsConfigType {
			continue
		}
		instance, err := settings.GetInstance()
		if err != nil {
			return fmt.Errorf("decoding tls settings: %w", err)
		}
		config := instance.(*tls.Config)
		config.Certificate = slices.DeleteFunc(config.Certificate, func(c *tls.Certificate) bool {
			return c.GetCertificatePath() != "" || c.GetKeyPath() != ""
		})
		stream.SecuritySettings[i] = serial.ToTypedMessage(config)
	}
	return nil
}

// withoutFileCertificates returns a copy of an inbound object without the tlsSettings certificate
// entries that name a file.
func withoutFileCertificates(raw map[string]any) (map[string]any, error) {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("encoding the file inbound: %w", err)
	}
	clone, err := decodeObject(encoded)
	if err != nil {
		return nil, err
	}
	tlsSettings := object(object(clone["streamSettings"])["tlsSettings"])
	certs, ok := tlsSettings["certificates"].([]any)
	if !ok {
		return clone, nil
	}
	tlsSettings["certificates"] = slices.DeleteFunc(certs, func(c any) bool {
		entry := object(c)
		file, _ := entry["certificateFile"].(string)
		key, _ := entry["keyFile"].(string)
		return file != "" || key != ""
	})
	return clone, nil
}
