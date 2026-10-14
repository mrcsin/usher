package xray

import (
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func staleSocket(t *testing.T) string {
	t.Helper()
	// t.TempDir paths exceed the unix socket path limit on macOS.
	dir, err := os.MkdirTemp("", "usher")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "x.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRemoveStaleSocket(t *testing.T) {
	t.Run("free lock removes the socket", func(t *testing.T) {
		path := staleSocket(t)
		removed, err := removeStaleSocket(path)
		if err != nil || !removed {
			t.Fatalf("removed=%v err=%v", removed, err)
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("socket still there: %v", err)
		}
	})
	t.Run("held lock keeps the socket", func(t *testing.T) {
		path := staleSocket(t)
		lock, err := os.OpenFile(path+".lock", os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
			t.Fatal(err)
		}
		removed, err := removeStaleSocket(path)
		if err != nil || removed {
			t.Fatalf("removed=%v err=%v", removed, err)
		}
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("socket gone: %v", err)
		}
	})
	t.Run("missing path is left alone", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "xray.sock")
		removed, err := removeStaleSocket(path)
		if err != nil || removed {
			t.Fatalf("removed=%v err=%v", removed, err)
		}
		if _, err := os.Lstat(path + ".lock"); !os.IsNotExist(err) {
			t.Fatalf("lock file created: %v", err)
		}
	})
}
