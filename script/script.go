// Package script hosts Craggy Starlark scripts: it runs a program top to
// bottom and then its entry function, under step and wall-clock limits, and
// turns whatever stops it into a classified *Error with positions and a
// backtrace.
//
// A program sees the Starlark universe (in the dialect of package dialect),
// a frozen `input` global holding its JSON input, the host's predeclared
// names, and load() through a modules.Loader when the host supplies one.
//
// Determinism: the host exposes no clock, no randomness and no I/O of its
// own. A script given the same input, the same host built-ins and the same
// modules computes the same result, which is what makes Result.Digest a
// meaningful content address. Hosts that want time, randomness or effects
// must inject them deliberately as predeclared built-ins, and accept that
// such scripts are no longer reproducible from their digest alone.
package script

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"go.starlark.net/resolve"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/craggy-io/script-services/dialect"
	"github.com/craggy-io/script-services/modules"
	"github.com/craggy-io/script-services/values"
)

// Limits bound one execution.
type Limits struct {
	// MaxSteps bounds Starlark execution steps. Time spent waiting inside a
	// built-in consumes no steps. 0 means unbounded.
	MaxSteps uint64
	// Timeout bounds wall-clock time on top of the caller's context. 0
	// means only the context bounds it.
	Timeout time.Duration
}

// DefaultLimits are sensible limits for one script run.
var DefaultLimits = Limits{MaxSteps: 5_000_000, Timeout: 10 * time.Minute}

// Position is a source location in a script.
type Position struct {
	File string `json:"file"` // file name as given to the parser
	Line int    `json:"line"` // 1-based line
	Col  int    `json:"col"`  // 1-based column
}

// PositionOf converts a Starlark position; an invalid one is the zero
// Position.
func PositionOf(p syntax.Position) Position {
	if !p.IsValid() {
		return Position{}
	}
	return Position{File: p.Filename(), Line: int(p.Line), Col: int(p.Col)}
}

// Frame is one call frame of a script error's backtrace.
type Frame struct {
	Func string   `json:"func"` // function name, "<toplevel>" for a module body
	Pos  Position `json:"pos"`  // current position in that function
}

// Program is one script for Exec: source plus what the host adds.
type Program struct {
	// Filename names the script in positions, and is the path relative
	// load() specs ("./x.star") resolve against. Defaults to "script.star".
	Filename string
	// Source is the program text, in the dialect of package dialect.
	Source []byte
	// Predeclared adds host built-ins. Naming one "input" is an invalid
	// program.
	Predeclared starlark.StringDict
	// Input is a JSON-shaped value (anything encoding/json marshals), bound
	// frozen to the global `input` and passed to the entry function.
	Input any
	// Entry is the function called after the module body runs, when the
	// script defines it: Entry(input), or Entry() if it takes no
	// parameters. Its return value is the Result. Defaults to "run".
	Entry string
	// Modules serves load(). When nil, load() fails with
	// modules.ErrNoSource.
	Modules *modules.Loader
}

// Options configure one Exec.
type Options struct {
	Limits Limits // step and wall-clock bounds
	// Print receives print() output with the position of the print call.
	// nil discards it.
	Print func(pos Position, msg string)
}

// Result is a finished Exec.
type Result struct {
	Value   json.RawMessage `json:"value"`   // the entry function's return value; null without one
	Steps   uint64          `json:"steps"`   // Starlark execution steps used by the program
	Digest  string          `json:"digest"`  // modules.Digest(source, direct deps): the program's content address
	Modules []modules.Ref   `json:"modules"` // every module loaded, transitively, sorted by path (provenance)
}

// Code classifies an Error.
type Code string

// Error codes.
const (
	// Invalid: the script, its input or a limit is at fault.
	Invalid Code = "invalid"
	// Cancelled: the caller's context ended.
	Cancelled Code = "cancelled"
)

// Error is a failed script: a compile error, a runtime error (including one
// a host built-in returned), or a limit.
type Error struct {
	Code      Code     `json:"code"`                // Invalid or Cancelled
	Message   string   `json:"message"`             // what stopped the script
	Pos       Position `json:"pos"`                 // innermost position in the script's own file; zero when unknown
	Backtrace []Frame  `json:"backtrace,omitempty"` // outermost first
	Steps     uint64   `json:"steps"`               // execution steps used before failing
	TimedOut  bool     `json:"timedOut,omitempty"`  // Limits.Timeout ran out
	cause     error
}

// Error formats the error as "code: file:line:col: message".
func (e *Error) Error() string {
	if e.Pos.Line > 0 {
		return fmt.Sprintf("%s: %s:%d:%d: %s", e.Code, e.Pos.File, e.Pos.Line, e.Pos.Col, e.Message)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap returns the original cause, so errors.As finds errors returned by
// host built-ins through Starlark's EvalError.
func (e *Error) Unwrap() error { return e.cause }

// timeLimitReason is the thread cancellation reason for Limits.Timeout.
const timeLimitReason = "time limit exceeded"

// TimeLimitMessage is Error.Message for a script stopped by Limits.Timeout.
const TimeLimitMessage = "script exceeded its time limit"

// NewThread returns a thread bounded by lim.MaxSteps whose print() goes to
// print (nil discards it). lim.Timeout is enforced by Run, not here.
func NewThread(name string, lim Limits, print func(*starlark.Thread, string)) *starlark.Thread {
	t := &starlark.Thread{Name: name}
	if lim.MaxSteps > 0 {
		t.SetMaxExecutionSteps(lim.MaxSteps)
	}
	if print != nil {
		t.Print = print
	} else {
		t.Print = func(*starlark.Thread, string) {}
	}
	return t
}

// Run calls fn, cancelling thread when ctx ends or lim.Timeout elapses. A
// timeout cancels the thread with the reason "time limit exceeded".
func Run(ctx context.Context, lim Limits, thread *starlark.Thread, fn func() (starlark.Value, error)) (starlark.Value, error) {
	v, err, _ := runTracked(ctx, lim, thread, fn)
	return v, err
}

// cancelKind records which of Run's cancellation sources fired first, so
// callers can tell a timeout apart from caller cancellation (and from a
// genuine error that merely returned late) without sniffing error text.
type cancelKind int32

const (
	cancelNone cancelKind = iota
	cancelContext
	cancelTimeout
)

func runTracked(ctx context.Context, lim Limits, thread *starlark.Thread, fn func() (starlark.Value, error)) (starlark.Value, error, cancelKind) {
	if ctx == nil {
		ctx = context.Background()
	}
	var kind atomic.Int32
	cancelWith := func(k cancelKind, reason string) {
		// Only the first source cancels the thread (thread.Cancel is
		// first-reason-wins too), so kind and the thread's reason agree.
		if kind.CompareAndSwap(int32(cancelNone), int32(k)) {
			thread.Cancel(reason)
		}
	}
	if err := ctx.Err(); err != nil {
		cancelWith(cancelContext, "context done: "+err.Error()) // run no code at all
	}
	stop := context.AfterFunc(ctx, func() { cancelWith(cancelContext, "context done: "+ctx.Err().Error()) })
	defer stop()
	if lim.Timeout > 0 {
		t := time.AfterFunc(lim.Timeout, func() { cancelWith(cancelTimeout, timeLimitReason) })
		defer t.Stop()
	}
	v, err := fn()
	return v, err, cancelKind(kind.Load())
}

// IsCancellation reports whether err's chain contains Starlark's
// cancellation error for the given thread.Cancel reason, as opposed to a
// genuine error (for example from a host built-in) that returned after a
// deadline. Module load failures are checked through *modules.Error.
// Hosts building on Run and Failure use it to compute Failure's timedOut
// argument exactly as Exec and Call do.
func IsCancellation(err error, reason string) bool {
	marker := "Starlark computation cancelled: " + reason
	for err != nil {
		switch e := err.(type) {
		case *starlark.EvalError:
			if strings.Contains(e.Msg, marker) {
				return true
			}
		case *modules.Error:
			if strings.Contains(e.Msg, marker) {
				return true
			}
		default:
			if err.Error() == marker {
				return true
			}
		}
		err = errors.Unwrap(err)
	}
	return false
}

// Call calls fn(*args) on a fresh thread under lim, and returns its result
// and the steps it used. A failure is a classified *Error (see Failure)
// positioned in fn's file.
func Call(ctx context.Context, lim Limits, name string, fn starlark.Callable, args starlark.Tuple, print func(*starlark.Thread, string)) (starlark.Value, uint64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	thread := NewThread(name, lim, print)
	v, err, kind := runTracked(ctx, lim, thread, func() (starlark.Value, error) {
		return starlark.Call(thread, fn, args, nil)
	})
	steps := thread.ExecutionSteps()
	if err != nil {
		file := ""
		if f, ok := fn.(*starlark.Function); ok {
			file = f.Position().Filename()
		}
		// A timeout only when the timer actually fired first and the
		// error is the resulting cancellation: a genuine error that
		// returned after the deadline keeps its own message.
		timedOut := kind == cancelTimeout && ctx.Err() == nil && IsCancellation(err, timeLimitReason)
		return nil, steps, Failure(ctx, file, err, timedOut, steps)
	}
	return v, steps, nil
}

// Failure classifies err from running filename into an *Error: positions and
// backtrace from Starlark errors, Cancelled when ctx has ended, else
// Invalid; timedOut replaces the message with TimeLimitMessage.
func Failure(ctx context.Context, filename string, err error, timedOut bool, steps uint64) *Error {
	var se *Error
	if errors.As(err, &se) && se == err {
		return se
	}
	out := &Error{Code: Invalid, Message: err.Error(), Steps: steps, cause: err}
	var (
		evalErr *starlark.EvalError
		synErr  syntax.Error
		resErrs resolve.ErrorList
	)
	switch {
	case errors.As(err, &evalErr):
		out.Message = evalErr.Msg
		for _, fr := range evalErr.CallStack {
			pos := PositionOf(fr.Pos)
			out.Backtrace = append(out.Backtrace, Frame{Func: fr.Name, Pos: pos})
			if pos.File == filename {
				out.Pos = pos
			}
		}
	case errors.As(err, &synErr):
		out.Message, out.Pos = synErr.Msg, PositionOf(synErr.Pos)
	case errors.As(err, &resErrs) && len(resErrs) > 0:
		// Pos is the first error's, so its message goes bare; later ones
		// keep their own "line:col: ".
		msgs := make([]string, len(resErrs))
		for i, e := range resErrs {
			if i == 0 {
				msgs[i] = e.Msg
				continue
			}
			msgs[i] = fmt.Sprintf("%d:%d: %s", e.Pos.Line, e.Pos.Col, e.Msg)
		}
		out.Message, out.Pos = strings.Join(msgs, "; "), PositionOf(resErrs[0].Pos)
	}
	switch {
	case ctx != nil && ctx.Err() != nil:
		out.Code = Cancelled
	case timedOut:
		out.TimedOut = true
		out.Message = TimeLimitMessage
	}
	return out
}

// Exec runs a program: the module body top to bottom, then the entry
// function if the program defines one. The entry's return value, converted
// to JSON, is Result.Value. Result.Modules lists every module the program
// loaded, transitively. Result.Digest is modules.Digest over p.Source and
// the program's direct dependencies (p.Modules.Deps): the same formula a
// module's digest uses, so the file has one digest either way, and
// transitive dependencies count through their parents' digests.
//
// Failures are *Error values: Cancelled when ctx ended, else Invalid (with
// TimedOut set when opts.Limits.Timeout ran out).
func Exec(ctx context.Context, p Program, opts Options) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	filename := p.Filename
	if filename == "" {
		filename = "script.star"
	}
	entry := p.Entry
	if entry == "" {
		entry = "run"
	}
	invalid := func(steps uint64, format string, args ...any) (Result, error) {
		msg := fmt.Sprintf(format, args...)
		return Result{Steps: steps}, &Error{Code: Invalid, Message: msg, Steps: steps, cause: errors.New(msg)}
	}
	inputJSON, err := json.Marshal(p.Input)
	if err != nil {
		return invalid(0, "input is not JSON-encodable: %v", err)
	}
	input, err := values.FromJSON(inputJSON)
	if err != nil {
		return invalid(0, "input: %v", err)
	}
	input.Freeze()
	predeclared := starlark.StringDict{"input": input}
	for name, v := range p.Predeclared {
		if name == "input" {
			return invalid(0, "host built-in %q collides with the input global", name)
		}
		predeclared[name] = v
	}

	// The timeout is a context deadline so module loads honour it too; it
	// is told apart from the caller's own cancellation below.
	runCtx, cancel := ctx, context.CancelFunc(func() {})
	if opts.Limits.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, opts.Limits.Timeout)
	}
	defer cancel()

	var print func(*starlark.Thread, string)
	if opts.Print != nil {
		print = func(th *starlark.Thread, msg string) { opts.Print(PositionOf(th.CallFrame(1).Pos), msg) }
	}
	thread := NewThread("exec:"+filename, Limits{MaxSteps: opts.Limits.MaxSteps}, print)
	if p.Modules != nil {
		p.Modules.Bind(runCtx, thread, filename)
	} else {
		thread.Load = func(*starlark.Thread, string) (starlark.StringDict, error) {
			return nil, modules.ErrNoSource
		}
	}
	value, err := Run(runCtx, Limits{}, thread, func() (starlark.Value, error) {
		prog, err := dialect.Compile(filename, p.Source, predeclared.Has)
		if err != nil {
			return nil, err
		}
		globals, err := prog.Init(thread, predeclared)
		globals.Freeze()
		if err != nil {
			return nil, err
		}
		fn, ok := globals[entry].(*starlark.Function)
		if !ok {
			return starlark.None, nil
		}
		args := starlark.Tuple{input}
		if fn.NumParams() == 0 {
			args = nil
		}
		return starlark.Call(thread, fn, args, nil)
	})
	convertedCancel := false
	if err == nil && runCtx.Err() != nil {
		err = runCtx.Err()
		convertedCancel = true
	}
	steps := thread.ExecutionSteps()
	if err != nil {
		// Timed out only when this Exec's own deadline (not the
		// caller's context) ended the run: either the run was
		// converted from a success that finished after it, or the
		// error is the deadline's cancellation. A genuine error that
		// merely returned after the deadline keeps its own message.
		timedOut := opts.Limits.Timeout > 0 && ctx.Err() == nil &&
			errors.Is(runCtx.Err(), context.DeadlineExceeded) &&
			(convertedCancel || IsCancellation(err, "context done: "+context.DeadlineExceeded.Error()))
		return Result{Steps: steps}, Failure(ctx, filename, err, timedOut, steps)
	}
	raw, err := values.ToJSON(value)
	if err != nil {
		return invalid(steps, "%s() result is not JSON-convertible: %v", entry, err)
	}
	res := Result{Value: raw, Steps: steps, Modules: []modules.Ref{}}
	var deps []modules.Dep
	if p.Modules != nil {
		res.Modules = p.Modules.Refs(thread)
		deps = p.Modules.Deps(thread)
	}
	res.Digest = modules.Digest(p.Source, deps)
	return res, nil
}
