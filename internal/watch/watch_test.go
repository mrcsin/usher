package watch

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testPoll   = 5 * time.Millisecond
	testSettle = 200 * time.Millisecond
	idleRefill = time.Hour
	quiet      = 200 * time.Millisecond
	deadline   = 3 * time.Second
)

type harness struct {
	path   string
	calls  atomic.Int32
	cancel context.CancelFunc
	done   chan struct{}
}

func start(t *testing.T, create bool, refill time.Duration, pass func(h *harness, call int32)) *harness {
	t.Helper()
	h := &harness{path: filepath.Join(t.TempDir(), "usher.yml"), done: make(chan struct{})}
	if create {
		h.write(t, 1)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	w := New(h.path, Intervals{Poll: testPoll, Settle: testSettle, Refill: refill}, func(context.Context) {
		call := h.calls.Add(1)
		if pass != nil {
			pass(h, call)
		}
	})
	go func() {
		defer close(h.done)
		w.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-h.done
	})
	return h
}

// write creates or touches the file and gives it a distinct modification time.
func (h *harness) write(t *testing.T, n int64) {
	t.Helper()
	if err := os.WriteFile(h.path, []byte("a: [b]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.touch(t, n)
}

func (h *harness) touch(t *testing.T, n int64) {
	t.Helper()
	mtime := time.Unix(1_700_000_000+n, 0)
	if err := os.Chtimes(h.path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) waitCalls(t *testing.T, want int32) {
	t.Helper()
	end := time.Now().Add(deadline)
	for h.calls.Load() < want {
		if time.Now().After(end) {
			t.Fatalf("calls = %d, want at least %d", h.calls.Load(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

func (h *harness) assertCallsStay(t *testing.T, want int32) {
	t.Helper()
	time.Sleep(quiet)
	if got := h.calls.Load(); got != want {
		t.Fatalf("calls = %d, want %d", got, want)
	}
}

func TestPassAtStart(t *testing.T) {
	tests := []struct {
		name   string
		create bool
	}{
		{"file present", true},
		{"file missing", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := start(t, tt.create, idleRefill, nil)
			h.waitCalls(t, 1)
			h.assertCallsStay(t, 1)
		})
	}
}

func TestChangedModTimeTriggersOnePass(t *testing.T) {
	h := start(t, true, idleRefill, nil)
	h.waitCalls(t, 1)

	h.touch(t, 2)
	h.waitCalls(t, 2)
	h.assertCallsStay(t, 2)
}

func TestChangeWaitsForSettleDelay(t *testing.T) {
	h := start(t, true, idleRefill, nil)
	h.waitCalls(t, 1)

	for n := int64(2); n < 14; n++ {
		h.touch(t, n)
		time.Sleep(testSettle / 4)
	}
	if got := h.calls.Load(); got != 1 {
		t.Fatalf("calls during churn = %d, want 1", got)
	}
	h.waitCalls(t, 2)
	h.assertCallsStay(t, 2)
}

func TestFileCreatedAfterStartTriggersOnePass(t *testing.T) {
	h := start(t, false, idleRefill, nil)
	h.waitCalls(t, 1)
	h.assertCallsStay(t, 1)

	h.write(t, 2)
	h.waitCalls(t, 2)
	h.assertCallsStay(t, 2)
}

func TestRefillTickTriggersWithoutChange(t *testing.T) {
	h := start(t, true, 20*time.Millisecond, nil)
	h.waitCalls(t, 3)
}

func TestTriggersDuringPassYieldOneFollowingPass(t *testing.T) {
	release := make(chan struct{})
	h := start(t, true, idleRefill, func(h *harness, call int32) {
		if call == 1 {
			<-release
		}
	})
	h.waitCalls(t, 1)

	h.touch(t, 2)
	time.Sleep(3 * testSettle)
	h.touch(t, 3)
	time.Sleep(3 * testSettle)
	h.assertCallsStay(t, 1)

	close(release)
	h.waitCalls(t, 2)
	h.assertCallsStay(t, 2)
}

func TestStopsOnContextCancel(t *testing.T) {
	h := start(t, true, 10*time.Millisecond, nil)
	h.waitCalls(t, 1)

	h.cancel()
	select {
	case <-h.done:
	case <-time.After(deadline):
		t.Fatal("Run did not return after cancel")
	}
	got := h.calls.Load()
	time.Sleep(quiet / 2)
	if h.calls.Load() != got {
		t.Fatal("pass ran after cancel")
	}
}
