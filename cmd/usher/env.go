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

type environment struct {
	pass.Settings
	Host      netip.Addr   // USHER_HOST
	DNS       []netip.Addr // USHER_DNS
	AWGSocket string       // USHER_AWG_SOCKET, empty without the awg backend

	XraySocket string // USHER_XRAY_SOCKET, empty without the Xray backend
	XrayConfig string // USHER_XRAY_CONFIG, set with XraySocket
}

func environmentFrom(getenv func(string) string) (environment, error) {
	host, err := parseIPv4("USHER_HOST", getenv("USHER_HOST"))
	if err != nil {
		return environment{}, err
	}

	var awgSocket, xraySocket, xrayConfig string
	var dns []netip.Addr
	if getenv("USHER_AWG_SOCKET") != "" {
		if awgSocket, err = parseAbsPath("USHER_AWG_SOCKET", getenv("USHER_AWG_SOCKET")); err != nil {
			return environment{}, err
		}
		if dns, err = parseDNS(getenv("USHER_DNS")); err != nil {
			return environment{}, err
		}
	} else if getenv("USHER_DNS") != "" {
		return environment{}, errors.New("USHER_DNS is set without USHER_AWG_SOCKET")
	}
	if getenv("USHER_XRAY_SOCKET") != "" {
		if xraySocket, err = parseAbsPath("USHER_XRAY_SOCKET", getenv("USHER_XRAY_SOCKET")); err != nil {
			return environment{}, err
		}
		if xrayConfig, err = parseAbsPath("USHER_XRAY_CONFIG", getenv("USHER_XRAY_CONFIG")); err != nil {
			return environment{}, err
		}
	} else if getenv("USHER_XRAY_CONFIG") != "" {
		return environment{}, errors.New("USHER_XRAY_CONFIG is set without USHER_XRAY_SOCKET")
	}
	if awgSocket == "" && xraySocket == "" {
		return environment{}, errors.New("USHER_AWG_SOCKET or USHER_XRAY_SOCKET is required")
	}

	return environment{
		Settings:  pass.Settings{ConfigPath: configPath, ClientsDir: clientsPath, StatePath: statePath},
		Host:      host,
		DNS:       dns,
		AWGSocket: awgSocket,

		XraySocket: xraySocket,
		XrayConfig: xrayConfig,
	}, nil
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

func parseAbsPath(name, value string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("%s: %q is not an absolute path", name, value)
	}
	return value, nil
}
