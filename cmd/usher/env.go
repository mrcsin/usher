package main

import (
	"errors"
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

// environment holds the server facts from the process environment and the fixed paths.
type environment struct {
	pass.Settings
	Host       netip.Addr   // USHER_HOST
	DNS        []netip.Addr // USHER_DNS, read only with USHER_AWG_SOCKET
	AWGSocket  string       // USHER_AWG_SOCKET, empty when the backend is off
	XraySocket string       // USHER_XRAY_SOCKET, empty when the backend is off
}

func environmentFrom(getenv func(string) string) (environment, error) {
	host, err := parseIPv4("USHER_HOST", getenv("USHER_HOST"))
	if err != nil {
		return environment{}, err
	}

	awgSocket, err := parseSocketPath("USHER_AWG_SOCKET", getenv("USHER_AWG_SOCKET"))
	if err != nil {
		return environment{}, err
	}
	xraySocket, err := parseSocketPath("USHER_XRAY_SOCKET", getenv("USHER_XRAY_SOCKET"))
	if err != nil {
		return environment{}, err
	}
	if awgSocket == "" && xraySocket == "" {
		return environment{}, errors.New("set USHER_AWG_SOCKET, USHER_XRAY_SOCKET or both")
	}

	var dns []netip.Addr
	if awgSocket != "" {
		dns, err = parseDNS(getenv("USHER_DNS"))
		if err != nil {
			return environment{}, err
		}
	}

	return environment{
		Settings:   pass.Settings{ConfigPath: configPath, ClientsDir: clientsPath, StatePath: statePath},
		Host:       host,
		DNS:        dns,
		AWGSocket:  awgSocket,
		XraySocket: xraySocket,
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

func parseDNS(value string) ([]netip.Addr, error) {
	var dns []netip.Addr
	for _, field := range strings.Split(value, ",") {
		addr, err := parseIPv4("USHER_DNS", strings.TrimSpace(field))
		if err != nil {
			return nil, err
		}
		dns = append(dns, addr)
	}
	return dns, nil
}

// parseSocketPath returns an empty path for an unset variable, which turns the backend off.
func parseSocketPath(name, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("%s: %q is not an absolute path", name, value)
	}
	return value, nil
}
