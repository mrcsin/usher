//go:build e2e

// Package xray drives usher against the official Xray image through deploy/compose.e2e-xray.yml.
// It needs Docker with compose and no kernel module; AGENTS.md, Test, runs it.
package xray

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	// repoDir is the repository root relative to this package; go test runs in the package dir.
	repoDir       = "../../.."
	composeFile   = "deploy/compose.e2e-xray.yml"
	clientProfile = "client"
	tag           = "vless-reality"
	targetURL     = "http://target/"
	targetBody    = "usher-e2e-target"
	proxyPort     = "8080"

	// The deadlines follow the production constants: 3 s file poll, 500 ms settle, 30 s refill.
	changeTimeout  = 10 * time.Second
	refillTimeout  = 60 * time.Second
	requestTimeout = 5 * time.Second
	// refillTick is a little longer than the 30 s refill interval.
	refillTick = 35 * time.Second
	// failedFetches is the number of consecutive failed fetches that prove a user is off.
	failedFetches = 3
)

// The bind-mount directories of the compose project, under scratchRoot.
const (
	scratchRoot = ".e2e-xray"
	configDir   = "config"
	clientsDir  = "clients"
	stateDir    = "state"
	sockDir     = "sock"
	clientDir   = "client"
)

var scratchDirs = []string{configDir, clientsDir, stateDir, sockDir, clientDir}

// proxies maps each user to the HTTP proxy of that user's client container.
var proxies = map[string]string{
	"alice": "172.30.98.11",
	"bob":   "172.30.98.12",
}

func e2ePath(elem ...string) string {
	return filepath.Join(append([]string{repoDir, scratchRoot}, elem...)...)
}

func TestMain(m *testing.M) {
	os.Exit(runMain(m))
}

// runMain creates the bind-mount directories as the test user, so dockerd does not create them
// as root and usher, which runs as the test user, can write them. A run that was killed leaves
// the project up, so runMain takes it down before it starts.
func runMain(m *testing.M) int {
	flag.Parse()
	if count := flag.Lookup("test.count").Value.String(); count != "1" {
		fmt.Fprintf(os.Stderr, "-count=%s: TestXray restarts the xray service, so it runs once per process; use -count=1\n", count)
		return 1
	}
	down()
	if err := os.RemoveAll(e2ePath()); err != nil {
		fmt.Fprintf(os.Stderr, "removing %s: %v\n", scratchRoot, err)
		return 1
	}
	for _, dir := range scratchDirs {
		if err := os.MkdirAll(e2ePath(dir), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "creating %s: %v\n", dir, err)
			return 1
		}
	}
	for user := range proxies {
		if err := os.MkdirAll(e2ePath(clientDir, user), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "creating client dir of %s: %v\n", user, err)
			return 1
		}
	}
	defer down()

	out, err := composeCmd("up", "-d", "--build", "--wait", "usher").CombinedOutput()
	fmt.Fprintf(os.Stderr, "docker compose up:\n%s\n", out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "docker compose up: %v\n", err)
		printLogs()
		return 1
	}
	code := m.Run()
	if code != 0 {
		printLogs()
	}
	return code
}

func down() {
	out, err := composeCmd("--profile", clientProfile, "down", "-v", "--remove-orphans", "--timeout", "10").CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "docker compose down: %v\n%s\n", err, out)
	}
}

func printLogs() {
	out, err := composeCmd("--profile", clientProfile, "logs", "--no-color").CombinedOutput()
	fmt.Fprintf(os.Stderr, "docker compose logs (err %v):\n%s\n", err, out)
}

// composeCmd builds docker compose on the e2e project, run from the repository root with the
// uid and gid of the test user, so usher can write the bind mounts and open the socket.
func composeCmd(args ...string) *exec.Cmd {
	cmd := exec.Command("docker", append([]string{"compose", "-f", composeFile}, args...)...)
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(),
		"E2E_UID="+strconv.Itoa(os.Getuid()),
		"E2E_GID="+strconv.Itoa(os.Getgid()))
	return cmd
}

func compose(t *testing.T, args ...string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := composeCmd(args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("docker compose %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
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
	waitFor(t, within(changeTimeout), "the API socket of xray", func() error {
		_, err := os.Stat(e2ePath(sockDir, "api.sock"))
		return err
	})
	writeUsherConfig(t, usherYAML("alice", "bob"))
	deadline := within(changeTimeout)
	for user := range proxies {
		var link string
		waitFor(t, deadline, "link file of "+user, func() error {
			var err error
			link, err = readLink(user)
			return err
		})
		writeClientConfig(t, user, link)
	}
	compose(t, "--profile", clientProfile, "up", "-d", "client-alice", "client-bob")
	for user, proxy := range proxies {
		waitFor(t, within(changeTimeout), "a page through the link of "+user, func() error {
			return fetch(proxy)
		})
	}
}

// testSwitchOff removes bob from usher.yml. Once usher logs the removal, a new connection through
// bob's link must keep failing while alice's link still works.
func testSwitchOff(t *testing.T) {
	writeUsherConfig(t, usherYAML("alice"))
	waitFor(t, within(changeTimeout), "the removal of bob in the usher log", func() error {
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
	compose(t, "restart", "xray")
	waitFor(t, within(refillTimeout), "a page through the link of alice after the restart", func() error {
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

// usherLogs returns the log of the usher container.
func usherLogs(t *testing.T) string {
	t.Helper()
	out, err := composeCmd("logs", "--no-color", "usher").CombinedOutput()
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

func writeUsherConfig(t *testing.T, content string) {
	t.Helper()
	dir := e2ePath(configDir)
	tmp := filepath.Join(dir, ".usher.yml.tmp")
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		t.Fatalf("writing usher.yml: %v", err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, "usher.yml")); err != nil {
		t.Fatalf("replacing usher.yml: %v", err)
	}
}

// readLink returns the rendered link of the user, or the reason it is not there as a mode 0600
// file yet.
func readLink(user string) (string, error) {
	path := e2ePath(clientsDir, user, tag+".txt")
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

// writeClientConfig builds the Xray client config of a user from the rendered link. pbk, sid and
// sni go into the config unchanged.
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
			"port":     8080,
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
	if err := os.WriteFile(e2ePath(clientDir, user, "config.json"), data, 0o644); err != nil {
		t.Fatalf("writing the client config of %s: %v", user, err)
	}
}

// fetch requests the target page through the HTTP proxy of one client container and reports why
// the request did not return the page with status 200.
func fetch(proxyHost string) error {
	proxy, err := url.Parse("http://" + net.JoinHostPort(proxyHost, proxyPort))
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

func within(timeout time.Duration) time.Time {
	return time.Now().Add(timeout)
}

func waitFor(t *testing.T, deadline time.Time, what string, check func() error) {
	t.Helper()
	var err error
	for time.Now().Before(deadline) {
		if err = check(); err == nil {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("no %s before the deadline: %v", what, err)
}
