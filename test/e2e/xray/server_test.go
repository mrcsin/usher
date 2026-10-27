//go:build e2e

package xray

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	tagHysteria = "hy2"
	tagReality  = "vless-reality"
	tagXHTTP    = "vless-xhttp"
	tagWS       = "vless-ws"
)

// The order of tags fixes the proxy port of each route.
var tags = []string{tagHysteria, tagReality, tagXHTTP, tagWS}

const (
	serverAddress = "172.30.98.2"
	targetHost    = "target"
	targetAddress = "172.30.98.3"
	// realityShortID is the shortIds entry of the config before the drift step changes it.
	realityShortID = "ab12"
	driftShortID   = "cd34"
)

const (
	wsCertFile = "certs/ws.pem"
	wsKeyFile  = "certs/ws.key"
)

type server struct {
	realityPrivateKey string
	hysteria, xhttp   leaf
	ws                leaf
}

func newServer(ca *authority) (*server, error) {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating the Reality key: %w", err)
	}
	s := &server{realityPrivateKey: base64.RawURLEncoding.EncodeToString(key.Bytes())}
	for name, target := range map[string]*leaf{"hy.e2e": &s.hysteria, "xh.e2e": &s.xhttp, "ws.e2e": &s.ws} {
		if *target, err = ca.issue(name); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// pemLines returns the line list Xray takes for an inline certificate.
func pemLines(data []byte) []string {
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func inlineCertificate(l leaf, serverName string, alpn ...string) map[string]any {
	return map[string]any{
		"serverName":   serverName,
		"alpn":         alpn,
		"certificates": []any{map[string]any{"certificate": pemLines(l.cert), "key": pemLines(l.key)}},
	}
}

func (s *server) config(shortID string) ([]byte, error) {
	usherBlock := map[string]any{"address": serverAddress}
	inbound := func(tag string, port int, protocol string, settings, stream map[string]any) map[string]any {
		return map[string]any{
			"tag": tag, "listen": "0.0.0.0", "port": port, "protocol": protocol,
			"settings": settings, "streamSettings": stream, "x-usher": usherBlock,
		}
	}
	config := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"api": map[string]any{
			"tag": "api", "listen": "/run/xray/api.sock,0660",
			"services": []string{"HandlerService", "StatsService"},
		},
		"stats": map[string]any{},
		"policy": map[string]any{"levels": map[string]any{"0": map[string]any{
			"statsUserUplink": true, "statsUserDownlink": true, "statsUserOnline": true,
		}}},
		"inbounds": []any{
			inbound(tagHysteria, 4443, "hysteria",
				map[string]any{"version": 2, "clients": []any{}},
				map[string]any{
					"network":          "hysteria",
					"security":         "tls",
					"tlsSettings":      inlineCertificate(s.hysteria, "hy.e2e", "h3"),
					"hysteriaSettings": map[string]any{"version": 2},
					"finalmask": map[string]any{"udp": []any{map[string]any{
						"type": "salamander", "settings": map[string]any{"password": "e2e-salamander"},
					}}},
				}),
			inbound(tagReality, 443, "vless",
				map[string]any{"clients": []any{}, "decryption": "none"},
				map[string]any{
					"network":  "raw",
					"security": "reality",
					"realitySettings": map[string]any{
						"target":      targetHost + ":443",
						"serverNames": []string{"target.e2e"},
						"privateKey":  s.realityPrivateKey,
						"shortIds":    []string{shortID},
					},
				}),
			inbound(tagXHTTP, 2053, "vless",
				map[string]any{"clients": []any{}, "decryption": "none"},
				map[string]any{
					"network":     "xhttp",
					"security":    "tls",
					"tlsSettings": inlineCertificate(s.xhttp, "xh.e2e", "h2", "http/1.1"),
					"xhttpSettings": map[string]any{
						"mode":               "auto",
						"path":               "/xh",
						"sessionIDKey":       "X-Sid",
						"sessionIDPlacement": "header",
						"xPaddingBytes":      "2000-3000",
					},
				}),
			inbound(tagWS, 2057, "vless",
				map[string]any{"clients": []any{}, "decryption": "none"},
				map[string]any{
					"network":  "ws",
					"security": "tls",
					"tlsSettings": map[string]any{
						"serverName": "ws.e2e",
						"alpn":       []string{"http/1.1"},
						"certificates": []any{map[string]any{
							"certificateFile": "/etc/xray/" + wsCertFile,
							"keyFile":         "/etc/xray/" + wsKeyFile,
						}},
					},
					"wsSettings": map[string]any{"path": "/ws"},
				}),
		},
		// Freedom blocks private destinations unless a final rule allows them.
		"outbounds": []any{map[string]any{
			"tag": "direct", "protocol": "freedom",
			"settings": map[string]any{"finalRules": []any{
				map[string]any{"action": "allow", "ip": []string{targetAddress + "/32"}},
			}},
		}},
	}
	return json.MarshalIndent(config, "", "  ")
}

func (s *server) writeConfig(dir, shortID string) error {
	data, err := s.config(shortID)
	if err != nil {
		return fmt.Errorf("encoding the server config: %w", err)
	}
	return replaceFile(filepath.Join(dir, "config.json"), data)
}

func (s *server) writeWSCertificate(dir string) error {
	if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(wsCertFile)), 0o755); err != nil {
		return err
	}
	if err := replaceFile(filepath.Join(dir, wsKeyFile), s.ws.key); err != nil {
		return err
	}
	return replaceFile(filepath.Join(dir, wsCertFile), s.ws.cert)
}

// replaceFile renames a temporary file over path, so no reader sees half a file.
func replaceFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
