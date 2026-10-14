package xray

import (
	"crypto/ecdh"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudflare/circl/sign/mldsa/mldsa65"

	"github.com/mrcsin/usher/internal/state"
)

const (
	// hopInterval is the udphop interval in seconds; libxray omits it from a link at this value.
	hopInterval = 30
	hopMode     = "intervalLocal,intervalRemote"
)

// clientOutbound returns the Xray client outbound for one user on the inbound. It copies only what
// the client needs from the server config and rebuilds the TLS and Reality blocks, so nothing
// secret or server-only reaches the result. host is the address clients dial unless x-usher sets
// one. The xhttp tuning is packed into extra, the one form both the .json and the share link carry.
func clientOutbound(in inbound, host string, entry state.XrayEntry) (map[string]any, error) {
	stream, err := clientStream(in, entry)
	if err != nil {
		return nil, err
	}
	if in.Hints.Address != "" {
		host = in.Hints.Address
	}
	settings := map[string]any{"address": host, "port": in.Hints.Port}
	protocols[in.Protocol].clientSettings(in, entry, settings)
	return map[string]any{
		"tag":            in.Tag,
		"protocol":       in.Protocol,
		"settings":       settings,
		"streamSettings": stream,
	}, nil
}

func clientStream(in inbound, entry state.XrayEntry) (map[string]any, error) {
	server := object(in.Raw["streamSettings"])
	stream := map[string]any{"network": in.Network, "security": in.Security}

	switch in.Network {
	case "ws":
		stream["wsSettings"] = pick(object(server["wsSettings"]), "path", "host")
	case "grpc":
		stream["grpcSettings"] = pick(object(server["grpcSettings"]), "serviceName", "authority")
	case "httpupgrade":
		stream["httpupgradeSettings"] = pick(object(server["httpupgradeSettings"]), "path", "host")
	case "xhttp":
		stream["xhttpSettings"] = xhttpSettings(object(server["xhttpSettings"]))
	case "hysteria":
		stream["hysteriaSettings"] = map[string]any{"version": 2, "auth": entry.Password}
		if mask := hysteriaMask(in, server); mask != nil {
			stream["finalmask"] = mask
		}
	}

	switch in.Security {
	case securityTLS:
		tls, err := clientTLS(in, object(server["tlsSettings"]))
		if err != nil {
			return nil, err
		}
		stream["tlsSettings"] = tls
	case securityReality:
		reality, err := clientReality(in, object(server["realitySettings"]))
		if err != nil {
			return nil, err
		}
		stream["realitySettings"] = reality
	}
	return stream, nil
}

// pick returns a copy of the listed keys of m that are present.
func pick(m map[string]any, keys ...string) map[string]any {
	out := map[string]any{}
	for _, k := range keys {
		if v, ok := m[k]; ok {
			out[k] = deepCopy(v)
		}
	}
	return out
}

// xhttpSettings keeps path, host and mode at the top. A server extra replaces every other field on
// the server, so it is mirrored verbatim; without one the other fields move into extra.
func xhttpSettings(server map[string]any) map[string]any {
	out := pick(server, "path", "host", "mode")
	if extra := object(server["extra"]); len(extra) > 0 {
		out["extra"] = deepCopy(extra)
		return out
	}
	extra := map[string]any{}
	for k, v := range server {
		if _, top := out[k]; !top && k != "extra" {
			extra[k] = deepCopy(v)
		}
	}
	if len(extra) > 0 {
		out["extra"] = extra
	}
	return out
}

// hysteriaMask returns the client finalmask: the salamander password of the server, then a udphop
// entry when x-usher.hop_ports is set. libxray reads a link only in that order.
func hysteriaMask(in inbound, server map[string]any) map[string]any {
	var udp []any
	serverUDP, _ := object(server["finalmask"])["udp"].([]any)
	for _, item := range serverUDP {
		entry := object(item)
		if lowerString(entry["type"]) == "salamander" {
			udp = append(udp, map[string]any{
				"type":     "salamander",
				"settings": pick(object(entry["settings"]), "password"),
			})
		}
	}
	if in.Hints.HopPorts != "" {
		udp = append(udp, map[string]any{
			"type": "udphop",
			"settings": map[string]any{
				"mode":        hopMode,
				"interval":    hopInterval,
				"remotePorts": in.Hints.HopPorts,
			},
		})
	}
	if len(udp) == 0 {
		return nil
	}
	return map[string]any{"udp": udp}
}

// clientTLS rebuilds the tlsSettings block: the name, the ALPN list, the fingerprint and, for
// trust "pin", the pinned leaf hash.
func clientTLS(in inbound, server map[string]any) (map[string]any, error) {
	tls := map[string]any{"serverName": in.ServerName, "fingerprint": in.Hints.Fingerprint}
	if alpn, ok := server["alpn"]; ok {
		tls["alpn"] = deepCopy(alpn)
	}
	if in.Hints.Trust != trustPin {
		return tls, nil
	}
	pin := in.Hints.PinSHA256
	if pin == "" {
		var err error
		if pin, err = inlineCertificatePin(server); err != nil {
			return nil, fmt.Errorf("inbound %q: x-usher.pin_sha256: %w", in.Tag, err)
		}
	}
	tls["pinnedPeerCertSha256"] = pin
	return tls, nil
}

// inlineCertificatePin returns the lower case hex SHA-256 of the first certificate that an inline
// certificates entry carries.
func inlineCertificatePin(tls map[string]any) (string, error) {
	certs, _ := tls["certificates"].([]any)
	for _, c := range certs {
		pemLines, _ := object(c)["certificate"].([]any)
		if len(pemLines) == 0 {
			continue
		}
		lines := make([]string, len(pemLines))
		for i, line := range pemLines {
			lines[i], _ = line.(string)
		}
		block, _ := pem.Decode([]byte(strings.Join(lines, "\n")))
		if block == nil || block.Type != "CERTIFICATE" {
			return "", errors.New("the inline certificate is not a PEM certificate")
		}
		sum := sha256.Sum256(block.Bytes)
		return hex.EncodeToString(sum[:]), nil
	}
	return "", errors.New("no inline certificate")
}

// realityKeys decodes the privateKey and the optional mldsa65Seed of a realitySettings object. A
// problem is returned as the field and a reason that never quotes a value, because Xray's own
// errors do. seed is nil without an mldsa65Seed.
func realityKeys(server map[string]any) (key *ecdh.PrivateKey, seed []byte, field, reason string) {
	private, _ := server["privateKey"].(string)
	raw, err := base64.RawURLEncoding.DecodeString(private)
	if err != nil {
		return nil, nil, "privateKey", "want base64url"
	}
	key, err = ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return nil, nil, "privateKey", "want a 32 byte X25519 key"
	}
	encodedSeed, _ := server["mldsa65Seed"].(string)
	if encodedSeed == "" {
		return key, nil, "", ""
	}
	seed, err = base64.RawURLEncoding.DecodeString(encodedSeed)
	if err != nil || len(seed) != mldsa65.SeedSize {
		return nil, nil, "mldsa65Seed", "want 32 bytes in base64url"
	}
	if encodedSeed == private {
		return nil, nil, "mldsa65Seed", "must differ from privateKey"
	}
	return key, seed, "", ""
}

// clientReality rebuilds the realitySettings block from the server's: the public key of its
// privateKey, its first short id and, with an mldsa65Seed, the matching verify key.
func clientReality(in inbound, server map[string]any) (map[string]any, error) {
	key, seed, field, reason := realityKeys(server)
	if field != "" {
		return nil, fmt.Errorf("inbound %q: streamSettings.realitySettings.%s: %s", in.Tag, field, reason)
	}

	shortID := ""
	if ids, _ := server["shortIds"].([]any); len(ids) > 0 {
		shortID, _ = ids[0].(string)
	}
	reality := map[string]any{
		"serverName":  in.ServerName,
		"publicKey":   base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()),
		"shortId":     shortID,
		"fingerprint": in.Hints.Fingerprint,
		"spiderX":     "/",
	}
	if seed != nil {
		public, _ := mldsa65.NewKeyFromSeed((*[mldsa65.SeedSize]byte)(seed))
		reality["mldsa65Verify"] = base64.RawURLEncoding.EncodeToString(public.Bytes())
	}
	return reality, nil
}

// deepCopy copies the maps and lists of a decoded JSON value, so a result never aliases inbound.Raw.
func deepCopy(v any) any {
	switch v := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, item := range v {
			out[k] = deepCopy(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = deepCopy(item)
		}
		return out
	default:
		return v
	}
}
