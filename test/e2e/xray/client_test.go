//go:build e2e

package xray

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xtls/libxray/share"
)

const (
	targetURL      = "http://" + targetHost + "/"
	targetBody     = "usher-e2e-target"
	firstProxyPort = 8000
	requestTimeout = 5 * time.Second
)

var proxyHosts = map[string]string{
	"alice": "172.30.98.11",
	"bob":   "172.30.98.12",
}

// route is the .json config or .txt share link of a user for an inbound, with its own proxy port.
type route struct {
	tag  string
	form string
	port int
}

func (r route) String() string { return r.tag + r.formSuffix() }

func (r route) formSuffix() string { return "." + r.form }

func routes() []route {
	var all []route
	for _, tag := range tags {
		for _, form := range []string{"json", "txt"} {
			all = append(all, route{tag: tag, form: form, port: firstProxyPort + len(all)})
		}
	}
	return all
}

func routesOf(tag string) []route {
	var among []route
	for _, r := range routes() {
		if r.tag == tag {
			among = append(among, r)
		}
	}
	return among
}

// writeClientConfig only adds useSystemCA, so the client trusts SSL_CERT_FILE.
func writeClientConfig(t *testing.T, user string) {
	t.Helper()
	var inbounds, outbounds, rules []any
	for _, r := range routes() {
		outbound, err := routeOutbound(user, r)
		if err != nil {
			t.Fatalf("outbound of %s for %s: %v", r, user, err)
		}
		outboundTag := "out-" + strconv.Itoa(r.port)
		inboundTag := "in-" + strconv.Itoa(r.port)
		outbound["tag"] = outboundTag
		if tls := nested(outbound, "streamSettings", "tlsSettings"); tls != nil {
			tls["useSystemCA"] = true
		}
		inbounds = append(inbounds, map[string]any{
			"tag": inboundTag, "listen": "0.0.0.0", "port": r.port, "protocol": "http",
		})
		outbounds = append(outbounds, outbound)
		rules = append(rules, map[string]any{
			"type": "field", "inboundTag": []string{inboundTag}, "outboundTag": outboundTag,
		})
	}
	outbounds = append(outbounds, map[string]any{"tag": "block", "protocol": "blackhole"})
	config := map[string]any{
		"log":       map[string]any{"loglevel": "warning"},
		"inbounds":  inbounds,
		"outbounds": outbounds,
		"routing":   map[string]any{"rules": rules},
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		t.Fatalf("encoding the client config of %s: %v", user, err)
	}
	if err := os.WriteFile(project.path(clientDir, user, "config.json"), data, 0o644); err != nil {
		t.Fatalf("writing the client config of %s: %v", user, err)
	}
}

func routeOutbound(user string, r route) (map[string]any, error) {
	data, err := os.ReadFile(project.path(clientsDir, user, r.tag+r.formSuffix()))
	if err != nil {
		return nil, err
	}
	if r.form == "txt" {
		data, err = share.ConvertShareLinksToXrayJson(strings.TrimSpace(string(data)), "")
		if err != nil {
			return nil, fmt.Errorf("converting the share link: %w", err)
		}
	}
	var config struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parsing the client file: %w", err)
	}
	if len(config.Outbounds) == 0 {
		return nil, errors.New("the client file has no outbound")
	}
	return config.Outbounds[0], nil
}

func nested(object map[string]any, keys ...string) map[string]any {
	for _, key := range keys {
		next, ok := object[key].(map[string]any)
		if !ok {
			return nil
		}
		object = next
	}
	return object
}

func waitForClientFiles(t *testing.T, user string, timeout time.Duration) {
	t.Helper()
	waitFor(t, timeout, "client files of "+user, func() error {
		for _, r := range routes() {
			path := project.path(clientsDir, user, r.tag+r.formSuffix())
			info, err := os.Stat(path)
			if err != nil {
				return err
			}
			if info.Mode().Perm() != 0o600 {
				return fmt.Errorf("mode of %s = %v, want 0600", filepath.Base(path), info.Mode().Perm())
			}
		}
		return nil
	})
}

func fetch(user string, r route) error {
	proxy, err := url.Parse("http://" + net.JoinHostPort(proxyHosts[user], strconv.Itoa(r.port)))
	if err != nil {
		return err
	}
	client := &http.Client{
		Timeout:   requestTimeout,
		Transport: &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true},
	}
	resp, err := client.Get(targetURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), targetBody) {
		return fmt.Errorf("body %q does not come from the target", body)
	}
	return nil
}

func fetchAll(user string, among []route) map[route]error {
	var mu sync.Mutex
	var wg sync.WaitGroup
	results := make(map[route]error, len(among))
	for _, r := range among {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := fetch(user, r)
			mu.Lock()
			results[r] = err
			mu.Unlock()
		}()
	}
	wg.Wait()
	return results
}

func allWork(user string, among []route) error {
	results := fetchAll(user, among)
	var errs []error
	for _, r := range among {
		if err := results[r]; err != nil {
			errs = append(errs, fmt.Errorf("%s of %s: %w", r, user, err))
		}
	}
	return errors.Join(errs...)
}

func proxyReachable(user string, r route) error {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(proxyHosts[user], strconv.Itoa(r.port)), requestTimeout)
	if err != nil {
		return fmt.Errorf("%s of %s: proxy is not reachable: %w", r, user, err)
	}
	return conn.Close()
}

func proxiesReachable(user string, among []route) error {
	var errs []error
	for _, r := range among {
		errs = append(errs, proxyReachable(user, r))
	}
	return errors.Join(errs...)
}

// noneWorks treats a refused connection to the proxy of a still-starting client as an error, not
// a failed route.
func noneWorks(user string, among []route) error {
	if err := proxiesReachable(user, among); err != nil {
		return err
	}
	results := fetchAll(user, among)
	var errs []error
	for _, r := range among {
		if results[r] == nil {
			errs = append(errs, fmt.Errorf("%s of %s still works", r, user))
		}
	}
	return errors.Join(errs...)
}
