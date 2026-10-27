//go:build e2e

// Package xray drives usher against the official Xray image through deploy/compose.e2e-xray.yml.
// It needs Docker with compose and no kernel module; AGENTS.md, Test, runs it.
package xray

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	repoDir       = "../../.."
	composeFile   = "deploy/compose.e2e-xray.yml"
	clientProfile = "client"
	// helperImage has a shell, kill and pidof.
	helperImage = "nginx:1.27-alpine"

	// The deadlines follow the production constants: 3 s file poll, 500 ms settle, 30 s refill.
	changeTimeout  = 10 * time.Second
	connectTimeout = 40 * time.Second
	driftTimeout   = 60 * time.Second
	restartTimeout = 45 * time.Second
	killTimeout    = 90 * time.Second
	// statsTimeout is longer than one refill tick, so a pass runs after the traffic.
	statsTimeout = 45 * time.Second
	// refillTick is a little longer than the 30 s refill interval.
	refillTick   = 35 * time.Second
	failedRounds = 3
	pollInterval = 500 * time.Millisecond
)

const (
	scratchRoot = ".e2e-xray"
	configDir   = "config"
	clientsDir  = "clients"
	stateDir    = "state"
	sockDir     = "sock"
	xrayDir     = "xray"
	clientDir   = "client"
	statusDir   = "status"
	statusFile  = "usher.prom"
)

type composeProject struct{}

var (
	project composeProject
	ca      *authority
	srv     *server
)

func (composeProject) path(elem ...string) string {
	return filepath.Join(append([]string{repoDir, scratchRoot}, elem...)...)
}

// cmd runs with the uid and gid of the test user, so usher can write the bind mounts.
func (composeProject) cmd(args ...string) *exec.Cmd {
	cmd := exec.Command("docker", append([]string{"compose", "-f", composeFile}, args...)...)
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(),
		"E2E_UID="+strconv.Itoa(os.Getuid()),
		"E2E_GID="+strconv.Itoa(os.Getgid()))
	return cmd
}

func (p composeProject) compose(t *testing.T, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := p.cmd(args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("docker compose %s: %v\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func (p composeProject) down() {
	out, err := p.cmd("--profile", clientProfile, "down", "-v", "--remove-orphans", "--timeout", "10").CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "docker compose down: %v\n%s\n", err, out)
	}
}

func (p composeProject) printLogs() {
	out, err := p.cmd("--profile", clientProfile, "logs", "--no-color").CombinedOutput()
	fmt.Fprintf(os.Stderr, "docker compose logs (err %v):\n%s\n", err, out)
}

func TestMain(m *testing.M) {
	os.Exit(runSuite(m))
}

// runSuite creates the bind mounts as the test user, not root, and first takes down a project
// that a killed run left up.
func runSuite(m *testing.M) int {
	flag.Parse()
	if count := flag.Lookup("test.count").Value.String(); count != "1" {
		fmt.Fprintf(os.Stderr, "-count=%s: the suite restarts a service, so it runs once per process; use -count=1\n", count)
		return 1
	}
	project.down()
	if err := os.RemoveAll(project.path()); err != nil {
		fmt.Fprintf(os.Stderr, "removing %s: %v\n", scratchRoot, err)
		return 1
	}
	dirs := []string{configDir, clientsDir, stateDir, sockDir, xrayDir, statusDir,
		filepath.Join(clientDir, "alice"), filepath.Join(clientDir, "bob")}
	for _, dir := range dirs {
		if err := os.MkdirAll(project.path(dir), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "creating %s: %v\n", dir, err)
			return 1
		}
	}
	if err := writeFixtures(); err != nil {
		fmt.Fprintln(os.Stderr, "writing fixtures:", err)
		return 1
	}
	defer project.down()

	out, err := project.cmd("up", "-d", "--build", "--wait", "usher").CombinedOutput()
	fmt.Fprintf(os.Stderr, "docker compose up:\n%s\n", out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "docker compose up: %v\n", err)
		project.printLogs()
		return 1
	}
	code := m.Run()
	if code != 0 {
		project.printLogs()
	}
	return code
}

func writeFixtures() error {
	var err error
	if ca, err = newAuthority(); err != nil {
		return err
	}
	if srv, err = newServer(ca); err != nil {
		return err
	}
	if err := os.WriteFile(project.path(clientDir, "ca.pem"), ca.pem, 0o644); err != nil {
		return err
	}
	if err := srv.writeWSCertificate(project.path(xrayDir)); err != nil {
		return err
	}
	return srv.writeConfig(project.path(xrayDir), realityShortID)
}

func waitFor(t *testing.T, timeout time.Duration, what string, check func() error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var err error
	for time.Now().Before(deadline) {
		if err = check(); err == nil {
			return
		}
		time.Sleep(pollInterval)
	}
	t.Fatalf("no %s within %v: %v", what, timeout, err)
}

func usherLogs(t *testing.T) string {
	t.Helper()
	out, err := project.cmd("logs", "--no-color", "usher").CombinedOutput()
	if err != nil {
		t.Fatalf("docker compose logs usher: %v\n%s", err, out)
	}
	return string(out)
}

// logMark is a position in the usher log, so a step can look at what usher logged after it.
type logMark int

func markLogs(t *testing.T) logMark {
	t.Helper()
	return logMark(len(usherLogs(t)))
}

func logsSince(t *testing.T, mark logMark) string {
	t.Helper()
	logs := usherLogs(t)
	if int(mark) > len(logs) {
		return logs
	}
	return logs[mark:]
}

func waitForLog(t *testing.T, mark logMark, timeout time.Duration, pattern string) {
	t.Helper()
	re := regexp.MustCompile(pattern)
	waitFor(t, timeout, "usher log line "+pattern, func() error {
		if !re.MatchString(logsSince(t, mark)) {
			return fmt.Errorf("no line matches yet")
		}
		return nil
	})
}

func usherYAML(users ...string) string {
	var b strings.Builder
	for _, user := range users {
		fmt.Fprintf(&b, "%s: [%s]\n", user, strings.Join(tags, ", "))
	}
	return b.String()
}

func writeUsherConfig(t *testing.T, users ...string) {
	t.Helper()
	if err := replaceFile(project.path(configDir, "usher.yml"), []byte(usherYAML(users...))); err != nil {
		t.Fatalf("writing usher.yml: %v", err)
	}
}

func startClient(t *testing.T, user string) {
	t.Helper()
	waitForClientFiles(t, user, changeTimeout)
	writeClientConfig(t, user)
	project.compose(t, "--profile", clientProfile, "up", "-d", "--force-recreate", "client-"+user)
}

// assertOffRepeatedly requires several rounds with no working route, so one error is not a removal.
func assertOffRepeatedly(t *testing.T, user string, among []route) {
	t.Helper()
	waitFor(t, connectTimeout, "the proxies of the client of "+user, func() error {
		return proxiesReachable(user, among)
	})
	for range failedRounds {
		if err := noneWorks(user, among); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
	}
}

func removedLine(tag, user string) string {
	return fmt.Sprintf(`users changed" interface=%s .*removed="?\[[^\]]*%s/%s[ \]]`, tag, tag, user)
}

func addedLine(tag, user string) string {
	return fmt.Sprintf(`users changed" interface=%s added="?\[[^\]]*%s/%s[ \]]`, tag, tag, user)
}

// TestXray runs its subtests in order; each starts from the state the previous one left.
func TestXray(t *testing.T) {
	steps := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"connect", testConnect},
		{"stats", testStats},
		{"switch off", testSwitchOff},
		{"restart", testRestart},
		{"hard kill", testHardKill},
		{"drift", testDrift},
		{"certificate rewrite", testCertificateRewrite},
	}
	for _, step := range steps {
		if !t.Run(step.name, step.run) {
			t.FailNow()
		}
	}
}

func testConnect(t *testing.T) {
	waitFor(t, changeTimeout, "the API socket of xray", func() error {
		_, err := os.Stat(project.path(sockDir, "api.sock"))
		return err
	})
	writeUsherConfig(t, "alice", "bob")
	for _, user := range []string{"alice", "bob"} {
		startClient(t, user)
	}
	for _, user := range []string{"alice", "bob"} {
		waitFor(t, connectTimeout, "pages through the files of "+user, func() error {
			return allWork(user, routes())
		})
	}
}

// testStats reads the status file after both users fetched through every inbound. alice holds one
// tunnel open through the Reality inbound, so she reads online there; the fetches of the connect
// step have closed their connections, so online is asserted only for that tunnel.
func testStats(t *testing.T) {
	held := routesOf(tagReality)[0]
	tunnel, err := openTunnel("alice", held)
	if err != nil {
		t.Fatalf("opening a tunnel: %v", err)
	}
	defer tunnel.Close()
	online := `usher_user_online{interface="` + held.tag + `",user="alice"}`

	waitFor(t, statsTimeout, "stats of every inbound in the status file", func() error {
		samples, err := readStatus(project.path(statusDir, statusFile))
		if err != nil {
			return err
		}
		var errs []error
		if got := samples[online]; got != 1 {
			errs = append(errs, fmt.Errorf("%s = %v, want 1", online, got))
		}
		for _, tag := range tags {
			up := `usher_interface_up{interface="` + tag + `"}`
			if got := samples[up]; got != 1 {
				errs = append(errs, fmt.Errorf("%s = %v, want 1", up, got))
			}
			for _, user := range []string{"alice", "bob"} {
				labels := `{interface="` + tag + `",user="` + user + `"}`
				for _, name := range []string{"usher_user_uplink_bytes_total", "usher_user_downlink_bytes_total"} {
					if got := samples[name+labels]; got <= 0 {
						errs = append(errs, fmt.Errorf("%s%s = %v, want above 0", name, labels, got))
					}
				}
			}
		}
		return errors.Join(errs...)
	})
}

// readStatus maps each sample of the status file, as name{labels}, to its value.
func readStatus(path string) (map[string]float64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	samples := map[string]float64{}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.LastIndexByte(line, ' ')
		if i < 0 {
			return nil, fmt.Errorf("status line %q has no value", line)
		}
		value, err := strconv.ParseFloat(line[i+1:], 64)
		if err != nil {
			return nil, fmt.Errorf("status line %q: %w", line, err)
		}
		samples[line[:i]] = value
	}
	return samples, nil
}

// testSwitchOff removes bob from usher.yml. Once usher logs the removal from every inbound, a new
// connection through any file of bob must keep failing while alice still works.
func testSwitchOff(t *testing.T) {
	mark := markLogs(t)
	writeUsherConfig(t, "alice")
	for _, tag := range tags {
		waitForLog(t, mark, changeTimeout, removedLine(tag, "bob"))
	}
	// Restarting the client drops connections that bob opened before the removal; the removal
	// holds for new connections.
	project.compose(t, "--profile", clientProfile, "restart", "client-bob")
	assertOffRepeatedly(t, "bob", routes())
	waitFor(t, changeTimeout, "pages through the files of alice after bob was removed", func() error {
		return allWork("alice", routes())
	})
}

// testRestart restarts xray, which empties its user list. usher must put alice back on its refill
// tick and leave bob out.
func testRestart(t *testing.T) {
	project.compose(t, "restart", "xray")
	waitFor(t, restartTimeout, "pages through the files of alice after the restart", func() error {
		return allWork("alice", routes())
	})
	assertOffRepeatedly(t, "bob", routes())
}

// testHardKill leaves a stale socket; the restarted xray cannot bind it until usher unlinks it.
func testHardKill(t *testing.T) {
	mark := markLogs(t)
	killXray(t)
	waitFor(t, killTimeout, "pages through the files of alice after the kill", func() error {
		return allWork("alice", routes())
	})
	if !strings.Contains(logsSince(t, mark), "removed stale xray socket") {
		t.Fatal("usher did not log that it removed the stale xray socket")
	}
	assertOffRepeatedly(t, "bob", routes())
}

// killXray sends SIGKILL from a helper container in the PID namespace of xray; exit status 137
// triggers the restart policy.
func killXray(t *testing.T) {
	t.Helper()
	id := strings.TrimSpace(project.compose(t, "ps", "-q", "xray"))
	if id == "" {
		t.Fatal("no xray container")
	}
	out, err := exec.Command("docker", "run", "--rm", "--pid=container:"+id,
		"--entrypoint", "sh", helperImage, "-c", "kill -9 $(pidof xray)").CombinedOutput()
	if err != nil {
		t.Fatalf("killing xray: %v\n%s", err, out)
	}
}

// testDrift changes shortIds without restarting xray: only the reality inbound reports drift, and
// a removal during the drift still reaches xray.
func testDrift(t *testing.T) {
	mark := markLogs(t)
	writeUsherConfig(t, "alice", "bob")
	for _, tag := range tags {
		waitForLog(t, mark, restartTimeout, addedLine(tag, "bob"))
	}
	startClient(t, "bob")
	waitFor(t, connectTimeout, "pages through the files of bob after he came back", func() error {
		return allWork("bob", routes())
	})

	mark = markLogs(t)
	if err := srv.writeConfig(project.path(xrayDir), driftShortID); err != nil {
		t.Fatal(err)
	}
	waitForLog(t, mark, driftTimeout,
		`interface failed" backend=xray interface=`+tagReality+` .*differs from the running Xray in`)
	for _, tag := range failedInterfaces(logsSince(t, mark)) {
		if tag != tagReality {
			t.Fatalf("inbound %s failed, only %s has drift", tag, tagReality)
		}
	}

	mark = markLogs(t)
	writeUsherConfig(t, "alice")
	waitForLog(t, mark, changeTimeout, removedLine(tagReality, "bob"))
	project.compose(t, "--profile", clientProfile, "restart", "client-bob")
	assertOffRepeatedly(t, "bob", routesOf(tagReality))
	waitFor(t, changeTimeout, "pages through the reality files of alice under drift", func() error {
		return allWork("alice", routesOf(tagReality))
	})

	// With the file back in step with xray, no tick fails. Adding bob proves that a pass ran and
	// opened xray after the file was restored.
	if err := srv.writeConfig(project.path(xrayDir), realityShortID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	mark = markLogs(t)
	time.Sleep(refillTick)
	writeUsherConfig(t, "alice", "bob")
	for _, tag := range tags {
		waitForLog(t, mark, changeTimeout, addedLine(tag, "bob"))
	}
	assertNoFailureSince(t, mark, "the drift was undone")
}

// assertNoFailureSince needs a logged change first: a successful pass logs nothing else.
func assertNoFailureSince(t *testing.T, mark logMark, what string) {
	t.Helper()
	time.Sleep(2 * time.Second) // the pass that logged the change finishes its other inbounds
	logs := logsSince(t, mark)
	for _, bad := range []string{"interface failed", "backend is down", "level=ERROR"} {
		if strings.Contains(logs, bad) {
			t.Fatalf("usher logged %q after %s:\n%s", bad, what, logs)
		}
	}
}

func failedInterfaces(logs string) []string {
	var failed []string
	for _, match := range regexp.MustCompile(`interface failed" backend=xray interface=(\S+)`).FindAllStringSubmatch(logs, -1) {
		failed = append(failed, match[1])
	}
	return failed
}

// testCertificateRewrite replaces the certificateFile of the ws inbound with a new leaf of the
// same CA. That is no drift, and the inbound keeps serving.
func testCertificateRewrite(t *testing.T) {
	fresh, err := ca.issue("ws.e2e")
	if err != nil {
		t.Fatal(err)
	}
	srv.ws = fresh
	mark := markLogs(t)
	if err := srv.writeWSCertificate(project.path(xrayDir)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(refillTick)
	// Removing bob proves that a pass ran and opened xray after the rewrite.
	writeUsherConfig(t, "alice")
	for _, tag := range tags {
		waitForLog(t, mark, changeTimeout, removedLine(tag, "bob"))
	}
	assertNoFailureSince(t, mark, "the certificate was rewritten")
	waitFor(t, restartTimeout, "a page through ws after the rewrite", func() error {
		return allWork("alice", routesOf(tagWS))
	})
}
