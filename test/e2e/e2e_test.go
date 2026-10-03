//go:build e2e

// Package e2e drives usher against the published awg-grpc image through deploy/compose.e2e.yml
// and the host amneziawg module. It needs Docker with compose and the module loaded; AGENTS.md,
// Test, runs it.
package e2e

import (
	"bytes"
	"encoding/base64"
	"flag"
	"fmt"
	"maps"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mrcsin/usher/internal/state"
)

const (
	// repoDir is the repository root relative to this package; go test runs in the package dir.
	repoDir       = "../.."
	composeFile   = "deploy/compose.e2e.yml"
	iface         = "awge2e0"
	clientProfile = "handshake"
	interfaceAddr = "10.99.0.1"
	subnet        = "10.99.0.0/24"

	// The deadlines follow the production constants: 3 s file poll, 500 ms settle, 30 s refill.
	changeTimeout = 10 * time.Second
	refillTimeout = 60 * time.Second
)

// The bind-mount directories of the compose project, under scratchRoot.
const (
	scratchRoot = ".e2e"
	configDir   = "config"
	clientsDir  = "clients"
	stateDir    = "state"
	clientDir   = "client"
)

var scratchDirs = []string{configDir, clientsDir, stateDir, clientDir}

var bothUsers = usherYAML("phone", "laptop")

func e2ePath(elem ...string) string {
	return filepath.Join(append([]string{repoDir, scratchRoot}, elem...)...)
}

func usherYAML(users ...string) string {
	var b strings.Builder
	for _, user := range users {
		fmt.Fprintf(&b, "%s: [%s]\n", user, iface)
	}
	return b.String()
}

// peerSet maps the public key of a peer, base64, to its allowed IPs.
type peerSet map[string]string

func TestMain(m *testing.M) {
	os.Exit(runMain(m))
}

// runMain creates the bind-mount directories as the test user, so dockerd does not create them
// as root and usher, which runs as the test user, can write them. A run that was killed leaves
// the project up, so runMain takes it down before it starts.
func runMain(m *testing.M) int {
	flag.Parse()
	if count := flag.Lookup("test.count").Value.String(); count != "1" {
		fmt.Fprintf(os.Stderr, "-count=%s: TestE2E restarts the awg service, so it runs once per process; use -count=1\n", count)
		return 1
	}
	down()
	if err := os.RemoveAll(e2ePath()); err != nil {
		fmt.Fprintf(os.Stderr, "removing .e2e: %v\n", err)
		return 1
	}
	for _, dir := range scratchDirs {
		if err := os.MkdirAll(e2ePath(dir), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "creating %s: %v\n", dir, err)
			return 1
		}
	}
	defer down()

	out, err := composeCmd("up", "-d", "--build", "--wait", "awg", "usher").CombinedOutput()
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

func run(cmd *exec.Cmd) (stdout, stderr string, err error) {
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return outBuf.String(), errBuf.String(), err
}

func compose(t *testing.T, args ...string) string {
	t.Helper()
	stdout, stderr, err := run(composeCmd(args...))
	if err != nil {
		t.Fatalf("docker compose %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout, stderr)
	}
	return stdout
}

// TestE2E runs its subtests in order; each one starts from the state the previous one left.
func TestE2E(t *testing.T) {
	steps := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"enroll", testEnroll},
		{"handshake", testHandshake},
		{"switch off", testSwitchOff},
		{"broken file and refill", testBrokenFileAndRefill},
	}
	for _, step := range steps {
		if !t.Run(step.name, step.run) {
			t.FailNow()
		}
	}
}

func testEnroll(t *testing.T) {
	writeUsherConfig(t, bothUsers)
	deadline := within(changeTimeout)
	for _, user := range []string{"phone", "laptop"} {
		waitFor(t, deadline, "client config of "+user, func() error {
			return checkClientFile(user)
		})
	}
	entries := loadEntries(t)
	want := peerSet{}
	addresses := map[netip.Addr]bool{}
	for _, user := range []string{"phone", "laptop"} {
		entry, ok := entries[state.EntryName(iface, user)]
		if !ok {
			t.Fatalf("users.json has no entry %s/%s", iface, user)
		}
		addr := entry.Address
		if !netip.MustParsePrefix(subnet).Contains(addr) || addr.String() == interfaceAddr {
			t.Errorf("address of %s = %s, want a host address in %s other than %s", user, addr, subnet, interfaceAddr)
		}
		if addresses[addr] {
			t.Errorf("address %s is held by two users", addr)
		}
		addresses[addr] = true
		want[publicKey(entry)] = entry.Route().String()
	}
	waitPeers(t, deadline, want)
}

// testHandshake proves that a copy of the phone's client config completes a handshake with the
// interface from a second container. The config differs from the original in two lines, as in
// awg-grpc's smoke test: the image has no resolvconf for DNS, and a full-tunnel AllowedIPs needs
// sysctl writes a non-privileged container cannot make.
func testHandshake(t *testing.T) {
	original, err := os.ReadFile(e2ePath(clientsDir, "phone", iface+".conf"))
	if err != nil {
		t.Fatalf("reading the phone config: %v", err)
	}
	conf := clientConfigCopy(t, string(original))
	path := e2ePath(clientDir, "awgc0.conf")
	if err := os.WriteFile(path, []byte(conf), 0o600); err != nil {
		t.Fatalf("writing client config: %v", err)
	}

	compose(t, "--profile", clientProfile, "up", "-d", "--wait", "client")
	defer removeClient(t)

	// The first packet into the tunnel starts the handshake; its reply may be lost to it.
	stdout, stderr, err := run(composeCmd("exec", "-T", "client", "ping", "-c", "1", "-W", "5", interfaceAddr))
	t.Logf("first ping %s (err %v):\n%s%s", interfaceAddr, err, stdout, stderr)

	phoneKey := publicKey(loadEntries(t)[state.EntryName(iface, "phone")])
	waitFor(t, within(changeTimeout), "handshake of phone", func() error {
		peers, err := dump()
		if err != nil {
			return err
		}
		if peers[phoneKey].handshake == 0 {
			return fmt.Errorf("latest handshake of phone is 0")
		}
		return nil
	})
}

func clientConfigCopy(t *testing.T, original string) string {
	t.Helper()
	var out []string
	replaced := false
	for _, line := range strings.Split(original, "\n") {
		switch {
		case strings.HasPrefix(line, "DNS = "):
			continue
		case strings.HasPrefix(line, "AllowedIPs = "):
			line = "AllowedIPs = " + interfaceAddr + "/32"
			replaced = true
		}
		out = append(out, line)
	}
	if !replaced {
		t.Fatal("the phone config has no AllowedIPs line")
	}
	return strings.Join(out, "\n")
}

func removeClient(t *testing.T) {
	t.Helper()
	if t.Failed() {
		out, err := composeCmd("--profile", clientProfile, "logs", "--no-color", "client").CombinedOutput()
		t.Logf("docker compose logs client (err %v):\n%s", err, out)
	}
	if out, err := composeCmd("--profile", clientProfile, "rm", "-sf", "client").CombinedOutput(); err != nil {
		t.Errorf("removing client: %v\n%s", err, out)
	}
}

func testSwitchOff(t *testing.T) {
	entries := loadEntries(t)
	phoneBefore := entries[state.EntryName(iface, "phone")]
	laptopBefore := entries[state.EntryName(iface, "laptop")]
	phoneKey := publicKey(phoneBefore)
	both := peerSet{
		phoneKey:                phoneBefore.Route().String(),
		publicKey(laptopBefore): laptopBefore.Route().String(),
	}

	writeUsherConfig(t, usherYAML("phone"))
	deadline := within(changeTimeout)
	waitPeers(t, deadline, peerSet{phoneKey: both[phoneKey]})
	waitFor(t, deadline, "removal of clients/laptop", func() error {
		if _, err := os.Stat(e2ePath(clientsDir, "laptop")); err == nil {
			return fmt.Errorf("clients/laptop still exists")
		}
		return nil
	})
	if got := loadEntries(t)[state.EntryName(iface, "laptop")]; got != laptopBefore {
		t.Fatalf("users.json entry of laptop changed while the user was off")
	}

	writeUsherConfig(t, bothUsers)
	deadline = within(changeTimeout)
	waitPeers(t, deadline, both)
	if got := loadEntries(t)[state.EntryName(iface, "laptop")]; got != laptopBefore {
		t.Fatalf("users.json entry of laptop changed after the user came back")
	}
	waitFor(t, deadline, "client config of laptop", func() error {
		return checkClientFile("laptop")
	})
}

// testBrokenFileAndRefill writes a file with a YAML syntax error and, once usher has logged the
// error, restarts the awg service, which empties its interfaces. usher must refill them from the
// last valid file on its refill ticker and keep clients/ as it was. The log line of the refill
// pass proves the restart emptied the kernel: its added list names both users.
func testBrokenFileAndRefill(t *testing.T) {
	defer writeUsherConfig(t, bothUsers)

	before, err := dump()
	if err != nil {
		t.Fatalf("reading the peer set: %v", err)
	}
	if len(before) != 2 {
		t.Fatalf("peer set before the restart has %d peers, want 2", len(before))
	}
	clientsBefore := readClients(t)

	writeUsherConfig(t, fmt.Sprintf("phone: [%s\nlaptop: [%s]\n", iface, iface))
	errorLine := regexp.MustCompile(`usher\.yml.*line \d+`)
	waitFor(t, within(changeTimeout), "usher.yml error in the usher log", func() error {
		return checkLog(errorLine, "")
	})

	// Docker takes --since at second precision, so the stamp may include the second before it.
	restartedAt := time.Now().UTC().Format(time.RFC3339)
	compose(t, "restart", "awg")

	deadline := within(refillTimeout)
	waitFor(t, deadline, "refill of the peer set", func() error {
		got, err := dump()
		if err != nil {
			return err
		}
		if !maps.Equal(got.allowed(), before.allowed()) {
			return fmt.Errorf("peer set %v, want %v", got.allowed(), before.allowed())
		}
		return nil
	})
	refilled := regexp.MustCompile(`peers changed.*added="?\[laptop phone\]`)
	waitFor(t, deadline, "refill line in the usher log", func() error {
		return checkLog(refilled, restartedAt)
	})

	if after := readClients(t); !maps.Equal(after, clientsBefore) {
		t.Errorf("clients/ changed while the file was broken")
	}
}

func checkLog(re *regexp.Regexp, since string) error {
	args := []string{"logs", "--no-color"}
	if since != "" {
		args = append(args, "--since", since)
	}
	stdout, stderr, err := run(composeCmd(append(args, "usher")...))
	if err != nil {
		return fmt.Errorf("docker compose logs: %w: %s", err, stderr)
	}
	if !re.MatchString(stdout) {
		return fmt.Errorf("no line matches %q", re)
	}
	return nil
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

// checkClientFile reports why clients/<user>/<iface>.conf is not a mode 0600 file yet.
func checkClientFile(user string) error {
	info, err := os.Stat(e2ePath(clientsDir, user, iface+".conf"))
	if err != nil {
		return err
	}
	if info.Mode().Perm() != 0o600 {
		return fmt.Errorf("mode of %s config = %v, want 0600", user, info.Mode().Perm())
	}
	return nil
}

func readClients(t *testing.T) map[string]string {
	t.Helper()
	root := e2ePath(clientsDir)
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		files[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatalf("reading clients/: %v", err)
	}
	return files
}

func loadEntries(t *testing.T) map[string]state.Entry {
	t.Helper()
	s, err := state.Load(e2ePath(stateDir, "users.json"))
	if err != nil {
		t.Fatalf("loading users.json: %v", err)
	}
	return s.AWG
}

func publicKey(e state.Entry) string {
	return base64.StdEncoding.EncodeToString(e.PublicKey())
}

// dumpPeer is one peer line of awg show dump.
type dumpPeer struct {
	allowedIPs string
	handshake  int64
}

type dumpSet map[string]dumpPeer

func (d dumpSet) allowed() peerSet {
	set := peerSet{}
	for key, p := range d {
		set[key] = p.allowedIPs
	}
	return set
}

// dump reads the peers of the interface from the wrapper container. The preshared key column is
// never kept.
func dump() (dumpSet, error) {
	stdout, stderr, err := run(composeCmd("exec", "-T", "awg", "awg", "show", iface, "dump"))
	if err != nil {
		return nil, fmt.Errorf("awg show dump: %w: %s", err, stderr)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	peers := dumpSet{}
	// The first line is the interface itself.
	for _, line := range lines[1:] {
		fields := strings.Split(line, "\t")
		if len(fields) < 5 {
			return nil, fmt.Errorf("peer line has %d fields, want at least 5", len(fields))
		}
		handshake, err := strconv.ParseInt(fields[4], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("latest handshake %q: %w", fields[4], err)
		}
		peers[fields[0]] = dumpPeer{allowedIPs: fields[3], handshake: handshake}
	}
	return peers, nil
}

func waitPeers(t *testing.T, deadline time.Time, want peerSet) {
	t.Helper()
	waitFor(t, deadline, fmt.Sprintf("a peer set of %d peers", len(want)), func() error {
		got, err := dump()
		if err != nil {
			return err
		}
		if !maps.Equal(got.allowed(), want) {
			return fmt.Errorf("peer set %v, want %v", got.allowed(), want)
		}
		return nil
	})
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
