// Package watch runs the reconcile pass when the config file changes and on a refill timer.
package watch

import (
	"context"
	"os"
	"time"
)

// Watcher calls a pass function once at start, after every settled change of a file and on every
// refill tick.
type Watcher struct {
	path      string
	intervals Intervals
	pass      func(context.Context)
}

// Intervals are the timings of a Watcher. The file is checked every Poll; a new modification time
// counts once it has stayed unchanged for Settle. The pass also runs every Refill.
type Intervals struct {
	Poll   time.Duration
	Settle time.Duration
	Refill time.Duration
}

// New returns a Watcher of the file at path.
func New(path string, intervals Intervals, pass func(context.Context)) *Watcher {
	return &Watcher{path: path, intervals: intervals, pass: pass}
}

// Run blocks until ctx is done. A missing file is not an error: the pass reports it, and the
// file triggers a pass once it appears. One goroutine does everything. A tick that fires during a
// pass waits in its ticker, which buffers one tick, so each source yields at most one following
// pass.
func (w *Watcher) Run(ctx context.Context) {
	last, _ := w.modTime()
	w.pass(ctx)

	poll := time.NewTicker(w.intervals.Poll)
	defer poll.Stop()
	refill := time.NewTicker(w.intervals.Refill)
	defer refill.Stop()

	// settled is nil unless a changed modification time (pending) waits to stay unchanged.
	var settled <-chan time.Time
	var pending time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-refill.C:
			w.pass(ctx)
		case <-poll.C:
			seen, ok := w.modTime()
			if settled == nil && ok && !seen.Equal(last) {
				pending = seen
				settled = time.After(w.intervals.Settle)
			}
		case <-settled:
			settled = nil
			now, ok := w.modTime()
			switch {
			case !ok:
			case now.Equal(pending):
				last = now
				w.pass(ctx)
			default:
				pending = now
				settled = time.After(w.intervals.Settle)
			}
		}
	}
}

func (w *Watcher) modTime() (time.Time, bool) {
	info, err := os.Stat(w.path)
	if err != nil {
		return time.Time{}, false
	}
	return info.ModTime(), true
}
