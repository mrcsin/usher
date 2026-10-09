package xray

import (
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/mrcsin/usher/internal/state"
)

// vlessLink builds the share link of XTLS/Xray-core discussion #716 for one user of a VLESS
// inbound. params are the transport and security parameters of the inbound.
func vlessLink(e state.XrayEntry, user string, host netip.Addr, port uint16, params url.Values) string {
	query := url.Values{"encryption": {decryptionNone}, "flow": {flowVision}}
	for key, values := range params {
		query[key] = values
	}
	address := net.JoinHostPort(host.String(), strconv.Itoa(int(port)))
	return "vless://" + e.ID.String() + "@" + address + "?" + encodeComponent(query.Encode()) + "#" + escapeComponent(user)
}

// escapeComponent escapes s as JavaScript's encodeURIComponent does, which the proposal requires.
func escapeComponent(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// encodeComponent turns the spaces that url.Values.Encode writes as "+" into "%20".
func encodeComponent(encoded string) string {
	return strings.ReplaceAll(encoded, "+", "%20")
}
