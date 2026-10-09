//go:build e2e

// Package e2e drives usher against the published awg-grpc image through deploy/compose.e2e.yml
// and the host amneziawg module. It needs Docker with compose and the module loaded; AGENTS.md,
// Test, runs it.
package e2e

import (
	"bytes"
	"encoding/base64"
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
	"github.com/mrcsin/usher/test/e2e/e2eutil"
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

var project = &e2eutil.Project{
	RepoDir:     repoDir,
	ComposeFile: composeFile,
	ScratchRoot: scratchRoot,
	Dirs:        []string{configDir, clientsDir, stateDir, clientDir},
	Profile:     clientProfile,
	Services:    []string{"awg", "usher"},
}

var bothUsers = usherYAML("phone", "laptop")

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
	os.Exit(project.Main(m))
}

func run(cmd *exec.Cmd) (stdout, stderr string, err error) {
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return outBuf.String(), errBuf.String(), err
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
	project.WriteConfig(t, bothUsers)
	deadline := e2eutil.Within(changeTimeout)
	for _, user := range []string{"phone", "laptop"} {
		e2eutil.WaitFor(t, deadline, "client config of "+user, func() error {
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
	original, err := os.ReadFile(project.Path(clientsDir, "phone", iface+".conf"))
	if err != nil {
		t.Fatalf("reading the phone config: %v", err)
	}
	conf := clientConfigCopy(t, string(original))
	path := project.Path(clientDir, "awgc0.conf")
	if err := os.WriteFile(path, []byte(conf), 0o600); err != nil {
		t.Fatalf("writing client config: %v", err)
	}

	project.Compose(t, "--profile", clientProfile, "up", "-d", "--wait", "client")
	defer removeClient(t)

	// The first packet into the tunnel starts the handshake; its reply may be lost to it.
	stdout, stderr, err := run(project.Cmd("exec", "-T", "client", "ping", "-c", "1", "-W", "5", interfaceAddr))
	t.Logf("first ping %s (err %v):\n%s%s", interfaceAddr, err, stdout, stderr)

	phoneKey := publicKey(loadEntries(t)[state.EntryName(iface, "phone")])
	e2eutil.WaitFor(t, e2eutil.Within(changeTimeout), "handshake of phone", func() error {
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
		out, err := project.Cmd("--profile", clientProfile, "logs", "--no-color", "client").CombinedOutput()
		t.Logf("docker compose logs client (err %v):\n%s", err, out)
	}
	if out, err := project.Cmd("--profile", clientProfile, "rm", "-sf", "client").CombinedOutput(); err != nil {
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

	project.WriteConfig(t, usherYAML("phone"))
	deadline := e2eutil.Within(changeTimeout)
	waitPeers(t, deadline, peerSet{phoneKey: both[phoneKey]})
	e2eutil.WaitFor(t, deadline, "removal of clients/laptop", func() error {
		if _, err := os.Stat(project.Path(clientsDir, "laptop")); err == nil {
			return fmt.Errorf("clients/laptop still exists")
		}
		return nil
	})
	if got := loadEntries(t)[state.EntryName(iface, "laptop")]; got != laptopBefore {
		t.Fatalf("users.json entry of laptop changed while the user was off")
	}

	project.WriteConfig(t, bothUsers)
	deadline = e2eutil.Within(changeTimeout)
	waitPeers(t, deadline, both)
	if got := loadEntries(t)[state.EntryName(iface, "laptop")]; got != laptopBefore {
		t.Fatalf("users.json entry of laptop changed after the user came back")
	}
	e2eutil.WaitFor(t, deadline, "client config of laptop", func() error {
		return checkClientFile("laptop")
	})
}

// testBrokenFileAndRefill writes a file with a YAML syntax error and, once usher has logged the
// error, restarts the awg service, which empties its interfaces. usher must refill them from the
// last valid file on its refill ticker and keep clients/ as it was. The log line of the refill
// pass proves the restart emptied the kernel: its added list names both users.
func testBrokenFileAndRefill(t *testing.T) {
	defer project.WriteConfig(t, bothUsers)

	before, err := dump()
	if err != nil {
		t.Fatalf("reading the peer set: %v", err)
	}
	if len(before) != 2 {
		t.Fatalf("peer set before the restart has %d peers, want 2", len(before))
	}
	clientsBefore := readClients(t)

	project.WriteConfig(t, fmt.Sprintf("phone: [%s\nlaptop: [%s]\n", iface, iface))
	errorLine := regexp.MustCompile(`usher\.yml.*line \d+`)
	e2eutil.WaitFor(t, e2eutil.Within(changeTimeout), "usher.yml error in the usher log", func() error {
		return checkLog(errorLine, "")
	})

	// Docker takes --since at second precision, so the stamp may include the second before it.
	restartedAt := time.Now().UTC().Format(time.RFC3339)
	project.Compose(t, "restart", "awg")

	deadline := e2eutil.Within(refillTimeout)
	e2eutil.WaitFor(t, deadline, "refill of the peer set", func() error {
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
	e2eutil.WaitFor(t, deadline, "refill line in the usher log", func() error {
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
	stdout, stderr, err := run(project.Cmd(append(args, "usher")...))
	if err != nil {
		return fmt.Errorf("docker compose logs: %w: %s", err, stderr)
	}
	if !re.MatchString(stdout) {
		return fmt.Errorf("no line matches %q", re)
	}
	return nil
}

// checkClientFile reports why clients/<user>/<iface>.conf is not a mode 0600 file yet.
func checkClientFile(user string) error {
	info, err := os.Stat(project.Path(clientsDir, user, iface+".conf"))
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
	root := project.Path(clientsDir)
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
	s, err := state.Load(project.Path(stateDir, "users.json"))
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
	stdout, stderr, err := run(project.Cmd("exec", "-T", "awg", "awg", "show", iface, "dump"))
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
	e2eutil.WaitFor(t, deadline, fmt.Sprintf("a peer set of %d peers", len(want)), func() error {
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
