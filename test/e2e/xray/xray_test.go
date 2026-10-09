//go:build e2e

// Package xray drives usher against the official Xray image through deploy/compose.e2e-xray.yml.
// It needs Docker with compose and no kernel module; AGENTS.md, Test, runs it.
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
	"testing"
	"time"

	"github.com/mrcsin/usher/test/e2e/e2eutil"
)

const (
	// repoDir is the repository root relative to this package; go test runs in the package dir.
	repoDir       = "../../.."
	composeFile   = "deploy/compose.e2e-xray.yml"
	clientProfile = "client"
	tag           = "vless-reality"
	targetURL     = "http://target/"
	targetBody    = "usher-e2e-target"
	proxyPort     = 8080

	changeTimeout  = 10 * time.Second
	refillTimeout  = 60 * time.Second
	requestTimeout = 5 * time.Second
	// refillTick is a little longer than the 30 s refill interval.
	refillTick = 35 * time.Second
	// failedFetches is the number of consecutive failed fetches that prove a user is off.
	failedFetches = 3
)

const (
	scratchRoot = ".e2e-xray"
	configDir   = "config"
	clientsDir  = "clients"
	stateDir    = "state"
	sockDir     = "sock"
	clientDir   = "client"
)

var project = &e2eutil.Project{
	RepoDir:     repoDir,
	ComposeFile: composeFile,
	ScratchRoot: scratchRoot,
	Dirs: []string{configDir, clientsDir, stateDir, sockDir,
		filepath.Join(clientDir, "alice"), filepath.Join(clientDir, "bob")},
	Profile:  clientProfile,
	Services: []string{"usher"},
}

// proxies maps each user to the HTTP proxy of that user's client container.
var proxies = map[string]string{
	"alice": "172.30.98.11",
	"bob":   "172.30.98.12",
}

func TestMain(m *testing.M) {
	os.Exit(project.Main(m))
}

// TestXray runs its subtests in order; each one starts from the state the previous one left.
func TestXray(t *testing.T) {
	steps := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"connect", testConnect},
		{"switch off", testSwitchOff},
		{"refill", testRefill},
	}
	for _, step := range steps {
		if !t.Run(step.name, step.run) {
			t.FailNow()
		}
	}
}

// testConnect writes a config of two users, builds a client from each rendered link and fetches
// the target page through it.
func testConnect(t *testing.T) {
	e2eutil.WaitFor(t, e2eutil.Within(changeTimeout), "the API socket of xray", func() error {
		_, err := os.Stat(project.Path(sockDir, "api.sock"))
		return err
	})
	project.WriteConfig(t, usherYAML("alice", "bob"))
	deadline := e2eutil.Within(changeTimeout)
	for user := range proxies {
		var link string
		e2eutil.WaitFor(t, deadline, "link file of "+user, func() error {
			var err error
			link, err = readLink(user)
			return err
		})
		writeClientConfig(t, user, link)
	}
	project.Compose(t, "--profile", clientProfile, "up", "-d", "client-alice", "client-bob")
	for user, proxy := range proxies {
		e2eutil.WaitFor(t, e2eutil.Within(changeTimeout), "a page through the link of "+user, func() error {
			return fetch(proxy)
		})
	}
}

// testSwitchOff removes bob from usher.yml. Once usher logs the removal, a new connection through
// bob's link must keep failing while alice's link still works.
func testSwitchOff(t *testing.T) {
	project.WriteConfig(t, usherYAML("alice"))
	e2eutil.WaitFor(t, e2eutil.Within(changeTimeout), "the removal of bob in the usher log", func() error {
		if !strings.Contains(usherLogs(t), "removed=[bob]") {
			return errors.New("no removed=[bob] line yet")
		}
		return nil
	})
	assertFailsRepeatedly(t, "bob after the removal", proxies["bob"])
	if err := fetch(proxies["alice"]); err != nil {
		t.Fatalf("link of alice after bob was removed: %v", err)
	}
}

// testRefill restarts xray, which empties its user list. usher must put alice back on its refill
// ticker and leave bob out. Once the list is full again, a refill tick must change nothing.
func testRefill(t *testing.T) {
	project.Compose(t, "restart", "xray")
	e2eutil.WaitFor(t, e2eutil.Within(refillTimeout), "a page through the link of alice after the restart", func() error {
		return fetch(proxies["alice"])
	})
	assertFailsRepeatedly(t, "bob after the restart", proxies["bob"])

	changes := strings.Count(usherLogs(t), "users changed")
	time.Sleep(refillTick)
	if got := strings.Count(usherLogs(t), "users changed"); got != changes {
		t.Fatalf("usher changed users on a steady refill tick: %d lines before, %d after", changes, got)
	}
}

// assertFailsRepeatedly requires several consecutive failed fetches, so one transient error
// cannot stand for a removed user.
func assertFailsRepeatedly(t *testing.T, what, proxy string) {
	t.Helper()
	for range failedFetches {
		if err := fetch(proxy); err == nil {
			t.Fatalf("the link of %s still works", what)
		}
		time.Sleep(time.Second)
	}
}

func usherLogs(t *testing.T) string {
	t.Helper()
	out, err := project.Cmd("logs", "--no-color", "usher").CombinedOutput()
	if err != nil {
		t.Fatalf("docker compose logs usher: %v\n%s", err, out)
	}
	return string(out)
}

func usherYAML(users ...string) string {
	var b strings.Builder
	for _, user := range users {
		fmt.Fprintf(&b, "%s: [%s]\n", user, tag)
	}
	return b.String()
}

// readLink returns the rendered link of the user, or the reason it is not there as a mode 0600
// file yet.
func readLink(user string) (string, error) {
	path := project.Path(clientsDir, user, tag+".txt")
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Mode().Perm() != 0o600 {
		return "", fmt.Errorf("mode of the link of %s = %v, want 0600", user, info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// writeClientConfig builds the Xray client config of a user from the rendered link.
func writeClientConfig(t *testing.T, user, link string) {
	t.Helper()
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatalf("parsing the link of %s: %v", user, err)
	}
	host, portText, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatalf("splitting the host of the link of %s: %v", user, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("port of the link of %s: %v", user, err)
	}
	query := parsed.Query()
	config := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{
			"listen":   "0.0.0.0",
			"port":     proxyPort,
			"protocol": "http",
		}},
		"outbounds": []any{map[string]any{
			"protocol": "vless",
			"settings": map[string]any{
				"address":    host,
				"port":       port,
				"id":         parsed.User.Username(),
				"flow":       query.Get("flow"),
				"encryption": query.Get("encryption"),
			},
			"streamSettings": map[string]any{
				"network":  "raw",
				"security": query.Get("security"),
				"realitySettings": map[string]any{
					"serverName":  query.Get("sni"),
					"fingerprint": query.Get("fp"),
					"publicKey":   query.Get("pbk"),
					"shortId":     query.Get("sid"),
				},
			},
		}},
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		t.Fatalf("encoding the client config of %s: %v", user, err)
	}
	if err := os.WriteFile(project.Path(clientDir, user, "config.json"), data, 0o644); err != nil {
		t.Fatalf("writing the client config of %s: %v", user, err)
	}
}

// fetch requests the target page through the HTTP proxy of one client container and reports why
// the request did not return the page with status 200.
func fetch(proxyHost string) error {
	proxy, err := url.Parse("http://" + net.JoinHostPort(proxyHost, strconv.Itoa(proxyPort)))
	if err != nil {
		return err
	}
	client := &http.Client{
		Timeout: requestTimeout,
		Transport: &http.Transport{
			Proxy:             http.ProxyURL(proxy),
			DisableKeepAlives: true,
		},
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
