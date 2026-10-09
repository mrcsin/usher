package main

import (
	"fmt"
	"net/netip"
	"path/filepath"
	"strings"

	"github.com/mrcsin/usher/internal/pass"
)

const (
	configPath  = "/srv/usher/config/usher.yml"
	clientsPath = "/srv/usher/clients"
	statePath   = "/srv/usher/state/users.json"
)

func settingsFrom(getenv func(string) string) (pass.Settings, error) {
	host, err := parseIPv4("USHER_HOST", getenv("USHER_HOST"))
	if err != nil {
		return pass.Settings{}, err
	}

	awgSocket, err := parseSocketPath("USHER_AWG_SOCKET", getenv("USHER_AWG_SOCKET"))
	if err != nil {
		return pass.Settings{}, err
	}

	var dns []netip.Addr
	for _, field := range strings.Split(getenv("USHER_DNS"), ",") {
		addr, err := parseIPv4("USHER_DNS", strings.TrimSpace(field))
		if err != nil {
			return pass.Settings{}, err
		}
		dns = append(dns, addr)
	}

	return pass.Settings{
		Host:       host,
		DNS:        dns,
		AWGSocket:  awgSocket,
		ConfigPath: configPath,
		ClientsDir: clientsPath,
		StatePath:  statePath,
	}, nil
}

func parseIPv4(name, value string) (netip.Addr, error) {
	if value == "" {
		return netip.Addr{}, fmt.Errorf("%s is required", name)
	}
	addr, err := netip.ParseAddr(value)
	if err != nil || !addr.Is4() {
		return netip.Addr{}, fmt.Errorf("%s: %q is not an IPv4 address", name, value)
	}
	return addr, nil
}

func parseSocketPath(name, value string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("%s: %q is not an absolute path", name, value)
	}
	return value, nil
}
