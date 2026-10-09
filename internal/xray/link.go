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
func vlessLink(e state.XrayEntry, user, flow string, host netip.Addr, port uint16, params url.Values) string {
	query := url.Values{"encryption": {decryptionNone}, "flow": {flow}}
	for key, values := range params {
		query[key] = values
	}
	address := net.JoinHostPort(host.String(), strconv.Itoa(int(port)))
	return "vless://" + e.ID.String() + "@" + address + "?" + escapeSpaces(query.Encode()) + "#" + escapeSpaces(url.QueryEscape(user))
}

// escapeSpaces turns the "+" that url.QueryEscape and url.Values.Encode write for a space into
// "%20", as JavaScript's encodeURIComponent does and the proposal requires.
func escapeSpaces(s string) string {
	return strings.ReplaceAll(s, "+", "%20")
}
