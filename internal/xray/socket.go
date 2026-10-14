package xray

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"syscall"
	"time"
)

// dialTimeout bounds the probe of a socket that has no lock file.
const dialTimeout = time.Second

// removeStaleSocket unlinks the API socket when no Xray listens on it. Xray holds an exclusive
// flock on <socket>.lock while it listens, so a lock that is free means a killed Xray left the
// socket behind and the next start would fail on it. A failed start removes the lock file, so a
// socket without one is probed instead: a refused connection means nobody listens. A live Xray
// accepts the probe, and its socket stays. It reports whether it removed the socket.
func removeStaleSocket(socketPath string) (bool, error) {
	before, err := os.Lstat(socketPath)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking xray socket: %w", err)
	}
	if before.Mode()&fs.ModeSocket == 0 {
		return false, fmt.Errorf("xray socket path %s is not a socket", socketPath)
	}

	// Xray creates the lock as root with mode 0644, so usher opens it read-only; flock needs no
	// write access.
	lock, err := os.Open(socketPath + ".lock")
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if !connectionRefused(socketPath) {
			return false, nil
		}
	case err != nil:
		return false, fmt.Errorf("opening xray socket lock: %w", err)
	default:
		defer lock.Close() // closing the file releases the flock
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			if errors.Is(err, syscall.EWOULDBLOCK) {
				return false, nil
			}
			return false, fmt.Errorf("locking xray socket: %w", err)
		}
	}

	// A new Xray may have replaced the socket while usher looked; leave a different file alone.
	after, err := os.Lstat(socketPath)
	if err != nil || !os.SameFile(before, after) {
		return false, nil
	}
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("removing stale xray socket: %w", err)
	}
	return true, nil
}

// connectionRefused reports whether a unix socket answers a connection with ECONNREFUSED, which is
// what a socket file without a listener does. Any other outcome counts as live.
func connectionRefused(socketPath string) bool {
	conn, err := net.DialTimeout("unix", socketPath, dialTimeout)
	if err == nil {
		_ = conn.Close()
		return false
	}
	return errors.Is(err, syscall.ECONNREFUSED)
}
