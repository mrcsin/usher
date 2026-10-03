// Package clients renders AmneziaWG client configs and mirrors them into the clients directory.
package clients

import (
	"encoding/base64"
	"fmt"
	"math"
	"net/netip"
	"strings"

	awgv1 "github.com/mrcsin/awg-grpc/gen/awg/v1"

	"github.com/mrcsin/usher/internal/state"
)

// Render returns the client config of one user on one interface. The error never quotes a
// client_params value.
func Render(entry state.Entry, iface *awgv1.InterfaceStatus, host netip.Addr, dns []netip.Addr) ([]byte, error) {
	port := iface.GetListenPort()
	if port > math.MaxUint16 {
		return nil, fmt.Errorf("listen port %d is not a port number", port)
	}

	var b strings.Builder
	b.WriteString("[Interface]\n")
	fmt.Fprintf(&b, "PrivateKey = %s\n", entry.PrivateKey)
	fmt.Fprintf(&b, "Address = %s\n", entry.Route())

	dnsList := make([]string, len(dns))
	for i, addr := range dns {
		dnsList[i] = addr.String()
	}
	fmt.Fprintf(&b, "DNS = %s\n", strings.Join(dnsList, ", "))

	for i, p := range iface.GetClientParams() {
		if strings.ContainsAny(p.GetKey(), "\r\n") || strings.ContainsAny(p.GetValue(), "\r\n") {
			return nil, fmt.Errorf("client_params entry %d contains a line break", i)
		}
		fmt.Fprintf(&b, "%s = %s\n", p.GetKey(), p.GetValue())
	}

	b.WriteString("\n[Peer]\n")
	fmt.Fprintf(&b, "PublicKey = %s\n", base64.StdEncoding.EncodeToString(iface.GetPublicKey()))
	fmt.Fprintf(&b, "PresharedKey = %s\n", entry.PresharedKey)
	fmt.Fprintf(&b, "Endpoint = %s\n", netip.AddrPortFrom(host, uint16(port)))
	b.WriteString("AllowedIPs = 0.0.0.0/0, ::/0\n")
	b.WriteString("PersistentKeepalive = 25\n")
	return []byte(b.String()), nil
}
