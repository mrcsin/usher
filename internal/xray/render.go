package xray

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/xtls/libxray/share"
	"github.com/xtls/xray-core/infra/conf"

	"github.com/mrcsin/usher/internal/clients"
	"github.com/mrcsin/usher/internal/state"
)

const (
	JSONSuffix = ".json"
	LinkSuffix = ".txt"

	localSocksPort = 10808
	localHTTPPort  = 10809
)

// renderJSON fails on an outbound Xray itself refuses to build, so no client gets one.
func renderJSON(outbound map[string]any) ([]byte, error) {
	config := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{
			map[string]any{
				"tag": "socks-in", "listen": "127.0.0.1", "port": localSocksPort,
				"protocol": "socks", "settings": map[string]any{"udp": true},
			},
			map[string]any{
				"tag": "http-in", "listen": "127.0.0.1", "port": localHTTPPort,
				"protocol": "http",
			},
		},
		"outbounds": []any{outbound},
	}
	encoded, err := json.Marshal(outbound)
	if err != nil {
		return nil, fmt.Errorf("encoding outbound: %w", err)
	}
	var detour conf.OutboundDetourConfig
	if err := json.Unmarshal(encoded, &detour); err != nil {
		return nil, fmt.Errorf("decoding outbound: %w", err)
	}
	if _, err := detour.Build(); err != nil {
		return nil, fmt.Errorf("building outbound: %w", err)
	}
	out, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding client config: %w", err)
	}
	return append(out, '\n'), nil
}

func renderLink(clientJSON []byte) ([]byte, error) {
	link, err := share.ConvertXrayJsonToShareLinks(clientJSON)
	if err != nil {
		return nil, err
	}
	return []byte(link + "\n"), nil
}

// renderFiles keeps the .json when libxray refuses the link: that is a usher defect, logged.
func renderFiles(log *slog.Logger, in inbound, host, user string, entry state.XrayEntry) (map[string][]byte, error) {
	outbound, err := clientOutbound(in, host, entry)
	if err != nil {
		return nil, err
	}
	clientJSON, err := renderJSON(outbound)
	if err != nil {
		return nil, fmt.Errorf("inbound %q: %w", in.Tag, err)
	}
	files := map[string][]byte{clients.Path(user, in.Tag, JSONSuffix): clientJSON}

	if in.Protocol == protocolHysteria && in.Hints.Trust == trustPin {
		return files, nil // v2rayNG and Hiddify do not read pcs for hysteria2://
	}
	link, err := renderLink(clientJSON)
	if err != nil {
		log.Error("libxray refused a share link; this is a defect in usher",
			"inbound", in.Tag, "user", user, "error", err)
		return files, nil
	}
	files[clients.Path(user, in.Tag, LinkSuffix)] = link
	return files, nil
}
