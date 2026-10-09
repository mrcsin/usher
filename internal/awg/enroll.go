package awg

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/netip"

	awgv1 "github.com/mrcsin/awg-grpc/gen/awg/v1"

	"github.com/mrcsin/usher/internal/state"
)

// enroll makes st hold a valid entry for every user in users on the interface. It creates missing
// entries (X25519 key pair, random PSK, lowest free address) and reassigns stored addresses that
// break the allocation rules. users must be sorted: of two entries with the same address the
// first keeps it. It reports whether any entry was created or changed. On error st is left as it
// was.
func enroll(log *slog.Logger, st *state.State, iface *awgv1.InterfaceStatus, users []string) (bool, error) {
	if len(users) == 0 {
		return false, nil
	}
	ifaceName := iface.GetName()
	prefix, reserved, err := interfaceNetwork(iface)
	if err != nil {
		return false, err
	}

	referenced := make(map[string]bool, len(users))
	for _, user := range users {
		referenced[user] = true
	}

	// Addresses held by entries of users who are switched off stay taken.
	taken := make(map[netip.Addr]bool)
	for user, e := range st.InterfaceEntries(ifaceName) {
		if !referenced[user] {
			taken[e.Address] = true
		}
	}

	// Every valid stored address is claimed before any new one is allocated, so a new user never
	// takes the address of an existing one.
	var needAddress []string
	for _, user := range users {
		entry := st.AWG[state.EntryName(ifaceName, user)]
		if usable(prefix, reserved, taken, entry.Address) {
			taken[entry.Address] = true
			continue
		}
		needAddress = append(needAddress, user)
	}

	updates := make(map[string]state.Entry, len(needAddress))
	for _, user := range needAddress {
		name := state.EntryName(ifaceName, user)
		addr, err := lowestFree(prefix, reserved, taken)
		if err != nil {
			return false, fmt.Errorf("user %s: %w", user, err)
		}
		taken[addr] = true
		entry, ok := st.AWG[name]
		if ok {
			log.Warn("reassigned address", "interface", ifaceName, "user", user,
				"old", entry.Address, "new", addr)
			entry.Address = addr
		} else {
			entry, err = newEntry(addr)
			if err != nil {
				return false, fmt.Errorf("user %s: %w", user, err)
			}
		}
		updates[name] = entry
	}

	maps.Copy(st.AWG, updates)
	return len(updates) > 0, nil
}

func interfaceNetwork(iface *awgv1.InterfaceStatus) (netip.Prefix, map[netip.Addr]bool, error) {
	var network netip.Prefix
	reserved := make(map[netip.Addr]bool, len(iface.GetAddresses()))
	for _, s := range iface.GetAddresses() {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, nil, fmt.Errorf("address %q is not a CIDR", s)
		}
		reserved[p.Addr()] = true
		if !network.IsValid() && p.Addr().Is4() {
			network = p.Masked()
		}
	}
	if !network.IsValid() {
		return netip.Prefix{}, nil, errors.New("has no IPv4 address")
	}
	return network, reserved, nil
}

// usable reports whether addr is a host address of prefix that nothing else holds.
func usable(prefix netip.Prefix, reserved, taken map[netip.Addr]bool, addr netip.Addr) bool {
	if !prefix.Contains(addr) || addr == prefix.Addr() || addr == broadcast(prefix) {
		return false
	}
	return !reserved[addr] && !taken[addr]
}

// lowestFree walks the prefix upward from its network address, so a prefix without host
// addresses (/31, /32) ends at once.
func lowestFree(prefix netip.Prefix, reserved, taken map[netip.Addr]bool) (netip.Addr, error) {
	for addr := prefix.Addr().Next(); prefix.Contains(addr); addr = addr.Next() {
		if usable(prefix, reserved, taken, addr) {
			return addr, nil
		}
	}
	return netip.Addr{}, fmt.Errorf("no free address in %s", prefix)
}

// broadcast returns the highest address of an IPv4 prefix.
func broadcast(prefix netip.Prefix) netip.Addr {
	network := prefix.Masked().Addr().As4()
	hostMask := uint32(1)<<(32-prefix.Bits()) - 1
	var out [4]byte
	binary.BigEndian.PutUint32(out[:], binary.BigEndian.Uint32(network[:])|hostMask)
	return netip.AddrFrom4(out)
}

func newEntry(addr netip.Addr) (state.Entry, error) {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return state.Entry{}, fmt.Errorf("generating private key: %w", err)
	}
	entry := state.Entry{Address: addr}
	copy(entry.PrivateKey[:], key.Bytes())
	if _, err := rand.Read(entry.PresharedKey[:]); err != nil {
		return state.Entry{}, fmt.Errorf("generating preshared key: %w", err)
	}
	return entry, nil
}
