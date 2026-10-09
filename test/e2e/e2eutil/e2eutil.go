//go:build e2e

// Package e2eutil holds what the e2e suites share: the docker compose project lifecycle, the
// scratch directories, the usher.yml writer and the polling helpers.
package e2eutil

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// pollInterval is the wait between two runs of a WaitFor check.
const pollInterval = 500 * time.Millisecond

// Project describes the compose project of one suite.
type Project struct {
	// RepoDir is the repository root relative to the suite's package directory.
	RepoDir string
	// ComposeFile is the compose file, relative to RepoDir.
	ComposeFile string
	// ScratchRoot is the directory under RepoDir that holds the bind mounts.
	ScratchRoot string
	// Dirs are the bind-mount directories under ScratchRoot.
	Dirs []string
	// Profile is the compose profile of the client containers.
	Profile string
	// Services are the services that Main starts and waits for.
	Services []string
}

// Path returns a path under the scratch root.
func (p *Project) Path(elem ...string) string {
	return filepath.Join(append([]string{p.RepoDir, p.ScratchRoot}, elem...)...)
}

// Cmd builds docker compose on the project, run from the repository root with the uid and gid of
// the test user, so usher can write the bind mounts and open the socket.
func (p *Project) Cmd(args ...string) *exec.Cmd {
	cmd := exec.Command("docker", append([]string{"compose", "-f", p.ComposeFile}, args...)...)
	cmd.Dir = p.RepoDir
	cmd.Env = append(os.Environ(),
		"E2E_UID="+strconv.Itoa(os.Getuid()),
		"E2E_GID="+strconv.Itoa(os.Getgid()))
	return cmd
}

// Compose runs docker compose on the project and returns its stdout; a failure ends the test.
func (p *Project) Compose(t *testing.T, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := p.Cmd(args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("docker compose %s: %v\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// Main runs the suite and returns its exit code. It creates the bind-mount directories as the
// test user, so dockerd does not create them as root and usher, which runs as the test user, can
// write them. A run that was killed leaves the project up, so Main takes it down before it starts.
func (p *Project) Main(m *testing.M) int {
	flag.Parse()
	if count := flag.Lookup("test.count").Value.String(); count != "1" {
		fmt.Fprintf(os.Stderr, "-count=%s: the suite restarts a service, so it runs once per process; use -count=1\n", count)
		return 1
	}
	p.down()
	if err := os.RemoveAll(p.Path()); err != nil {
		fmt.Fprintf(os.Stderr, "removing %s: %v\n", p.ScratchRoot, err)
		return 1
	}
	for _, dir := range p.Dirs {
		if err := os.MkdirAll(p.Path(dir), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "creating %s: %v\n", dir, err)
			return 1
		}
	}
	defer p.down()

	up := append([]string{"up", "-d", "--build", "--wait"}, p.Services...)
	out, err := p.Cmd(up...).CombinedOutput()
	fmt.Fprintf(os.Stderr, "docker compose up:\n%s\n", out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "docker compose up: %v\n", err)
		p.printLogs()
		return 1
	}
	code := m.Run()
	if code != 0 {
		p.printLogs()
	}
	return code
}

func (p *Project) down() {
	out, err := p.Cmd("--profile", p.Profile, "down", "-v", "--remove-orphans", "--timeout", "10").CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "docker compose down: %v\n%s\n", err, out)
	}
}

func (p *Project) printLogs() {
	out, err := p.Cmd("--profile", p.Profile, "logs", "--no-color").CombinedOutput()
	fmt.Fprintf(os.Stderr, "docker compose logs (err %v):\n%s\n", err, out)
}

// WriteConfig replaces config/usher.yml atomically with content.
func (p *Project) WriteConfig(t *testing.T, content string) {
	t.Helper()
	dir := p.Path("config")
	tmp := filepath.Join(dir, ".usher.yml.tmp")
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		t.Fatalf("writing usher.yml: %v", err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, "usher.yml")); err != nil {
		t.Fatalf("replacing usher.yml: %v", err)
	}
}

// Within returns the deadline that is timeout from now.
func Within(timeout time.Duration) time.Time {
	return time.Now().Add(timeout)
}

// WaitFor runs check until it returns nil or the deadline passes; then it ends the test with the
// last error.
func WaitFor(t *testing.T, deadline time.Time, what string, check func() error) {
	t.Helper()
	var err error
	for time.Now().Before(deadline) {
		if err = check(); err == nil {
			return
		}
		time.Sleep(pollInterval)
	}
	t.Fatalf("no %s before the deadline: %v", what, err)
}
