// Package memlimit bounds the memory one Starlark thread holds.
//
// go.starlark.net counts steps but not allocations, so the bound is kept by
// measuring: at step checkpoints (every few loop back-edges and calls) the
// thread compares how much the whole process has allocated since it last
// measured with the room it has left. Only when the process could have
// handed it more than its limit does it walk what it actually holds: every
// value reachable from its call stack (each frame's locals and operand
// stack, the frames' module globals, closures and bound methods). A thread
// over its limit is cancelled with Reason, which script.Failure reports as
// script.MemoryLimitMessage.
//
// What it bounds, and what it doesn't:
//   - It bounds what a thread keeps: lists, dicts, strings and the rest built
//     up across steps, including a comprehension's accumulator, which lives
//     only on the operand stack. Garbage a thread made and dropped isn't
//     held, so it doesn't count.
//   - A thread can overshoot by what it allocates between two measurements:
//     about 16 loop iterations or calls, or one long operation. One
//     operation is atomic to the interpreter: "x" * n allocates up to
//     go.starlark.net's own cap (1 GiB) before any checkpoint runs, and a
//     built-in such as str.replace or join is bounded only by its operands.
//     The host must leave room for that beside its limits.
//   - It costs a tight loop of arithmetic about 15% (BenchmarkCheckpoint);
//     a walk costs time in proportion to what is held, and a thread walks
//     at most a third of the time.
//   - Memory a host holds on a thread's behalf (a built-in's Go state, what
//     its capabilities buffer) is the host's to bound.
//   - The process-wide allocation counter makes the check cheap, not
//     approximate: a thread never allocates more than the whole process, so
//     skipping the walk is safe. Other threads' allocation only makes a
//     thread measure sooner, never cancels it: cancellation is decided by
//     what the thread itself holds.
//
// Reading each frame's operand stack relies on go.starlark.net's frame
// layout (a frame's locals and its stack share one slice), and finding a
// closure's captured variables on its cell type. Both are checked by
// reflection, falling back to locals only and to not following cells;
// TestHeldSeesAComprehension and the closure case of
// TestAThreadThatHoldsTooMuchStops fail when go.starlark.net changes them.
package memlimit

import (
	"fmt"
	"runtime/metrics"
	"time"

	"go.starlark.net/starlark"
)

// Reason is the thread.Cancel reason for a thread over its limit. The
// cancellation's message is "Starlark computation cancelled: " + Reason,
// followed by what the thread held.
const Reason = "memory limit exceeded"

// sampleEvery is how many checkpoints pass between reads of the process's
// allocation counter (a read costs about as much as a few hundred steps).
const sampleEvery = 4

// checkEvery is how many steps pass between checkpoints: 4 back-edges or
// calls (the interpreter counts 10 steps for each).
const checkEvery = 40

// Install makes thread stop after maxSteps execution steps (0: unbounded),
// exactly as thread.SetMaxExecutionSteps(maxSteps) does, and, when max > 0,
// once the values it holds pass about max bytes. With max <= 0 it is just
// SetMaxExecutionSteps. Call it before the thread runs; it takes over
// thread.OnMaxSteps.
func Install(thread *starlark.Thread, max int64, maxSteps uint64) {
	if max <= 0 {
		if maxSteps > 0 {
			thread.SetMaxExecutionSteps(maxSteps)
		}
		return
	}
	w := &watch{max: max, maxSteps: maxSteps, sample: []metrics.Sample{{Name: allocsMetric}}}
	w.allocs = w.read()
	thread.OnMaxSteps = w.checkpoint
	thread.SetMaxExecutionSteps(1)
}

const allocsMetric = "/gc/heap/allocs:bytes"

type watch struct {
	max      int64
	maxSteps uint64
	sample   []metrics.Sample

	ticks     int
	held      int64     // what the thread held when it last measured
	allocs    uint64    // the process's allocations then
	notBefore time.Time // the next walk, after a slow one
}

func (w *watch) read() uint64 {
	metrics.Read(w.sample)
	if w.sample[0].Value.Kind() != metrics.KindUint64 {
		return 0
	}
	return w.sample[0].Value.Uint64()
}

// checkpoint runs on the thread's own goroutine at every step check.
func (w *watch) checkpoint(thread *starlark.Thread) {
	if w.maxSteps > 0 && thread.Steps >= w.maxSteps {
		thread.Cancel("too many steps")
		return
	}
	// The next check is a few back-edges or calls on.
	next := thread.Steps + checkEvery
	if w.maxSteps > 0 && next > w.maxSteps {
		next = w.maxSteps
	}
	thread.SetMaxExecutionSteps(next)

	w.ticks++
	if w.ticks%sampleEvery != 0 {
		return
	}
	allocs := w.read()
	if allocs >= w.allocs && allocs-w.allocs <= uint64(max(w.max-w.held, 0)) {
		return // it can't hold more than it held plus all the process allocated
	}
	now := time.Now()
	if now.Before(w.notBefore) {
		return
	}
	held := Held(thread)
	// Walking costs time in proportion to what is held: a thread near its
	// limit in a busy process measures at most a third of the time.
	w.notBefore = now.Add(2 * time.Since(now))
	w.held, w.allocs = held, allocs
	if held > w.max {
		thread.Cancel(fmt.Sprintf("%s (held about %s of values, limit %s)", Reason, MiB(held), MiB(w.max)))
	}
}

// MiB formats a byte count for messages.
func MiB(n int64) string {
	if n < 10<<20 {
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d MiB", n>>20)
}
