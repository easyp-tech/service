package main

import (
	"fmt"
	"os"
	"sync"
)

// batchLabels are the words one batch reports its outcomes with. Push and
// register differ in nothing else: both walk a list of plugin versions
// concurrently, and both end up saying that an item went through, was already
// there, or failed.
type batchLabels struct {
	// inProgress names the action in a failure line: "Error pushing X: ...".
	inProgress string
	// skipReason says why an item needed no work: "already in storage".
	skipReason string
	// succeeded names the action in a success line: "Successfully pushed X".
	succeeded string
}

// batchTracker keeps concurrency-safe counters for a batch running in parallel,
// and is the only thing writing to the terminal while it does. Workers report
// through it rather than printing for themselves: interleaved writes from a
// dozen goroutines produce a garbled progress line and unreadable output.
type batchTracker struct {
	mu          sync.Mutex
	labels      batchLabels
	total       int
	completed   int
	succeeded   int
	skipped     int
	failed      int
	interactive bool
	spinIdx     int
}

func newBatchTracker(total int, interactive bool, labels batchLabels) *batchTracker {
	return &batchTracker{total: total, interactive: interactive, labels: labels}
}

// finish records one item's outcome and reports it.
func (t *batchTracker) finish(name string, wasSkipped bool, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.completed++

	switch {
	case err != nil:
		t.failed++
	case wasSkipped:
		t.skipped++
	default:
		t.succeeded++
	}

	if t.interactive {
		t.renderLocked()

		return
	}

	switch {
	case err != nil:
		_, _ = fmt.Fprintf(os.Stderr, "Error %s %s: %v\n", t.labels.inProgress, name, err)
	case wasSkipped:
		_, _ = fmt.Fprintf(os.Stdout, "Skipped (%s): %s\n", t.labels.skipReason, name)
	default:
		_, _ = fmt.Fprintf(os.Stdout, "Successfully %s %s\n", t.labels.succeeded, name)
	}
}

// renderLocked redraws the single progress line. The caller holds the mutex.
//
// Names of the items in flight are deliberately absent: with work running
// concurrently there is no one current item, and picking one to display would
// suggest the others are not running.
func (t *batchTracker) renderLocked() {
	spinners := getSpinners()
	spinner := spinners[t.spinIdx%len(spinners)]
	t.spinIdx++

	pct := int(float64(t.completed) / float64(t.total) * percentMultiplier)
	_, _ = fmt.Fprintf(
		os.Stdout,
		"\r\033[K%s %s %d%% (%d/%d) | ✅ %d  ⏭ %d  ❌ %d",
		spinner,
		renderProgressBar(pct),
		pct,
		t.completed,
		t.total,
		t.succeeded,
		t.skipped,
		t.failed,
	)
}

// done closes the progress line before the summary is printed.
func (t *batchTracker) done() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.interactive {
		return
	}

	_, _ = fmt.Fprintf(
		os.Stdout,
		"\r\033[K✓ %s Done! %d/%d\n",
		renderProgressBar(percentMultiplier),
		t.completed,
		t.total,
	)
}

// snapshot returns the final tally: total, succeeded, skipped, failed.
func (t *batchTracker) snapshot() (int, int, int, int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.total, t.succeeded, t.skipped, t.failed
}
