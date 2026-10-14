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

const dialTimeout = time.Second

// removeStaleSocket removes the socket only when the flock on <socket>.lock is free or, without
// the lock file, the probe gets ECONNREFUSED; a live Xray keeps its socket.
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

	// Xray creates the lock as root, mode 0644: open it read-only.
	lock, err := os.Open(socketPath + ".lock")
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if !connectionRefused(socketPath) {
			return false, nil
		}
	case err != nil:
		return false, fmt.Errorf("opening xray socket lock: %w", err)
	default:
		defer lock.Close()
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

// connectionRefused treats any outcome other than ECONNREFUSED as live.
func connectionRefused(socketPath string) bool {
	conn, err := net.DialTimeout("unix", socketPath, dialTimeout)
	if err == nil {
		_ = conn.Close()
		return false
	}
	return errors.Is(err, syscall.ECONNREFUSED)
}
