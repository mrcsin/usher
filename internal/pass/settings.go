// Package pass runs one reconcile pass: enrollment, rendering and the requests to awg-grpc.
package pass

import "net/netip"

// Settings holds the server facts and the paths one pass works with.
type Settings struct {
	Host       netip.Addr   // USHER_HOST
	DNS        []netip.Addr // USHER_DNS
	AWGSocket  string       // USHER_AWG_SOCKET
	ConfigPath string
	ClientsDir string
	StatePath  string
}
