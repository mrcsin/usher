// Package status renders the per-user stats of a pass as a Prometheus text exposition file.
package status

import (
	"bytes"
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// UserStats is the traffic and online state of one user on one interface.
type UserStats struct {
	Uplink   uint64 // bytes from the client
	Downlink uint64 // bytes to the client
	Online   bool
}

// Interface is the outcome of a pass for one interface. Users is nil when Prepare or Stats failed.
type Interface struct {
	Name  string
	Up    bool
	Users map[string]UserStats
}

type family struct {
	name, help, kind string
}

var (
	interfaceUp   = family{"usher_interface_up", "1 when the last pass applied the interface.", "gauge"}
	userOnline    = family{"usher_user_online", "1 when the user is connected now.", "gauge"}
	userUplink    = family{"usher_user_uplink_bytes_total", "Bytes received from the user.", "counter"}
	userDownlink  = family{"usher_user_downlink_bytes_total", "Bytes sent to the user.", "counter"}
	labelEscaping = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
)

type sample struct {
	labels string
	value  string
}

// Render returns the exposition file for interfaces, sorted by interface and user name.
// A metric family without samples is left out.
func Render(interfaces []Interface) []byte {
	sorted := slices.SortedFunc(slices.Values(interfaces), func(a, b Interface) int {
		return cmp.Compare(a.Name, b.Name)
	})
	var up, online, uplink, downlink []sample
	for _, iface := range sorted {
		ifaceLabel := `interface="` + labelEscaping.Replace(iface.Name) + `"`
		up = append(up, sample{ifaceLabel, boolValue(iface.Up)})
		for _, user := range slices.Sorted(maps.Keys(iface.Users)) {
			stats := iface.Users[user]
			labels := ifaceLabel + `,user="` + labelEscaping.Replace(user) + `"`
			online = append(online, sample{labels, boolValue(stats.Online)})
			uplink = append(uplink, sample{labels, strconv.FormatUint(stats.Uplink, 10)})
			downlink = append(downlink, sample{labels, strconv.FormatUint(stats.Downlink, 10)})
		}
	}
	var b bytes.Buffer
	writeFamily(&b, interfaceUp, up)
	writeFamily(&b, userOnline, online)
	writeFamily(&b, userUplink, uplink)
	writeFamily(&b, userDownlink, downlink)
	return b.Bytes()
}

func writeFamily(b *bytes.Buffer, f family, samples []sample) {
	if len(samples) == 0 {
		return
	}
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n", f.name, f.help, f.name, f.kind)
	for _, s := range samples {
		fmt.Fprintf(b, "%s{%s} %s\n", f.name, s.labels, s.value)
	}
}

func boolValue(v bool) string {
	if v {
		return "1"
	}
	return "0"
}
