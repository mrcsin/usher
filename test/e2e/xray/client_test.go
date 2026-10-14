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

// proxyHosts maps each user to the address of that user's client container.
var proxyHosts = map[string]string{
	"alice": "172.30.98.11",
	"bob":   "172.30.98.12",
}

// route is one way to reach the target: the client file of a user for an inbound, either the
// rendered .json config or the .txt share link. Each route has its own proxy port on the client.
type route struct {
	tag  string
	form string
	port int
}

func (r route) String() string { return r.tag + r.formSuffix() }

func (r route) formSuffix() string { return "." + r.form }

// routes lists a route for every inbound and form, in the port order of the client config.
func routes() []route {
	var all []route
	for _, tag := range tags {
		for _, form := range []string{"json", "txt"} {
			all = append(all, route{tag: tag, form: form, port: firstProxyPort + len(all)})
		}
	}
	return all
}

// routesOf returns the routes of one inbound.
func routesOf(tag string) []route {
	var among []route
	for _, r := range routes() {
		if r.tag == tag {
			among = append(among, r)
		}
	}
	return among
}

// writeClientConfig builds the Xray client config of a user from the rendered files of every
// inbound: one outbound and one HTTP proxy inbound per route. The test only adds useSystemCA to
// the TLS settings, so the client trusts SSL_CERT_FILE.
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

// routeOutbound returns the outbound that the rendered file of the route defines.
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

// nested returns the object at the path of keys, or nil when any step is missing.
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

// waitForClientFiles waits until usher has written the .json and .txt of the user for every
// inbound.
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

// fetch requests the target page through the proxy of the route and reports why the request did
// not return the page with status 200.
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

// fetchAll fetches through the given routes of a user at once and returns the error of each.
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

// allWork returns nil when every route of the list returns the page.
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

// noneWorks returns nil when every route of the list fails to return the page.
func noneWorks(user string, among []route) error {
	results := fetchAll(user, among)
	var errs []error
	for _, r := range among {
		if results[r] == nil {
			errs = append(errs, fmt.Errorf("%s of %s still works", r, user))
		}
	}
	return errors.Join(errs...)
}
