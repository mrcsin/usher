package xray

import (
	"bytes"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"

	"google.golang.org/protobuf/proto"

	"github.com/mrcsin/usher/gen/xray/common/serial"
	"github.com/mrcsin/usher/gen/xray/transport/internet/reality"
)

// securityNone names an empty security type in errors.
const securityNone = "none"

// fingerprintChrome is the TLS client fingerprint of every link; Reality requires one.
const fingerprintChrome = "chrome"

// securityReality is the security parameter of a Reality link.
const securityReality = "reality"

// securityDecoder returns the link parameters of a stream's security.
type securityDecoder func(settings []*serial.TypedMessage) (url.Values, error)

// securities maps a stream's security type, the full proto name of its settings message, to its
// decoder.
var securities = map[string]securityDecoder{
	typeName(&reality.Config{}): decodeReality,
}

// decodeReality derives the link parameters from the server's Reality settings. The private key
// stays inside this function; only its public key leaves.
func decodeReality(settings []*serial.TypedMessage) (url.Values, error) {
	if len(settings) != 1 || settings[0].GetType() != typeName(&reality.Config{}) {
		return nil, errors.New("reality settings are missing")
	}
	var config reality.Config
	if err := proto.Unmarshal(settings[0].GetValue(), &config); err != nil {
		return nil, fmt.Errorf("decoding reality settings: %w", err)
	}
	private, err := ecdh.X25519().NewPrivateKey(config.GetPrivateKey())
	if err != nil {
		return nil, errors.New("reality private_key is not an X25519 key")
	}
	serverName := firstNonEmpty(config.GetServerNames())
	if serverName == "" {
		return nil, errors.New("reality server_names has no name")
	}
	if len(config.GetShortIds()) == 0 {
		return nil, errors.New("reality short_ids is empty")
	}
	return url.Values{
		"security": {securityReality},
		"sni":      {serverName},
		"fp":       {fingerprintChrome},
		"pbk":      {base64.RawURLEncoding.EncodeToString(private.PublicKey().Bytes())},
		"sid":      {hex.EncodeToString(bytes.TrimRight(config.GetShortIds()[0], "\x00"))},
	}, nil
}

func firstNonEmpty(values []string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
