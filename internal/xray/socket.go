package xray

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// removeStaleSocket unlinks the API socket when no Xray holds its lock. Xray holds an exclusive
// flock on <socket>.lock while it listens, so a lock that is free means a killed Xray left the
// socket behind and the next start would fail on it. It reports whether it removed the socket.
func removeStaleSocket(socketPath string) (bool, error) {
	if _, err := os.Lstat(socketPath); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("checking xray socket: %w", err)
	}
	// Xray creates the lock as root with mode 0644, so usher opens it read-only; flock needs no
	// write access. A socket without a lock file has no live owner either.
	lock, err := os.Open(socketPath + ".lock")
	switch {
	case errors.Is(err, fs.ErrNotExist):
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
		defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck // closing the file releases it
	}
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("removing stale xray socket: %w", err)
	}
	return true, nil
}
