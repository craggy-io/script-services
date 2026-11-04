# Script

[Project](../README.md) · [Documentation index](README.md) · [Modules](modules.md) · [Check](check.md)

Available Go API: `github.com/craggy-io/script-services/script`.

## Purpose

Host Craggy Starlark scripts: run a program top to bottom and then its entry
function, under step and wall-clock limits, and turn whatever stops it into a
classified `*script.Error` with positions and a backtrace.

## Features

- `Exec` runs a program with a frozen JSON `input`, host built-ins, and `load()`
  through a `modules.Loader`; it returns the entry function's result as JSON.
- Step and wall-clock limits; the time limit also covers module loads.
- Two error codes, `Invalid` and `Cancelled`, plus `TimedOut`; positions,
  backtraces and JSON-marshalable errors.
- `Unwrap` reaches host built-in errors through Starlark's `EvalError`.
- A content digest over the source and its direct `load()`s (each spec paired
  with the loaded module's digest), equal to the file's digest as a module.
- Building blocks for other runners: `NewThread`, `Run`, `Call`, `Failure`.

## Mental model

A program is a notebook: its module body runs top to bottom (top-level loops,
prints and host calls included), then, if it defines one, the entry function
(`run` by default) is called with the input. The entry's return value is the
result. Nothing in the runtime reads a clock, draws randomness or performs I/O,
so the same source, input, modules and host built-ins compute the same result,
which is what makes `Result.Digest` a meaningful content address.

## Responsibilities and boundaries

`script` owns one execution: the thread, limits, input/result conversion, error
classification and the digest. Hosts own what the program can touch
(`Program.Predeclared`), where modules come from (`Program.Modules`), and what a
failure means to their callers. `script` knows nothing about registries,
capabilities or tools.

A program sees:

- the Starlark universe, in the [dialect](dialect.md);
- `input`: `Program.Input` marshalled with `encoding/json`, converted by
  [values](values.md) and frozen;
- `Program.Predeclared` (naming one `input` is an invalid program);
- `load()` through `Program.Modules`, or failing with `modules.ErrNoSource` when
  it is nil.

The entry function is `Program.Entry` (default `"run"`). If the global by that
name is a Starlark function, it is called with `input`, or with no arguments if it
declares no parameters. If the program defines no such function (or binds the
name to something else), the result is `null`.

## Public interfaces

- `Exec(ctx context.Context, p Program, opts Options) (Result, error)`
- `type Program struct { Filename string; Source []byte; Predeclared starlark.StringDict; Input any; Entry string; Modules *modules.Loader }`
- `type Options struct { Limits Limits; Print func(pos Position, msg string) }`
- `type Limits struct { MaxSteps uint64; Timeout time.Duration }`; `var DefaultLimits` (5,000,000 steps, 10 minutes)
- `type Result struct { Value json.RawMessage; Steps uint64; Digest string; Modules []modules.Ref }`
- `type Error struct { Code Code; Message string; Pos Position; Backtrace []Frame; Steps uint64; TimedOut bool }` with `Unwrap`
- `type Code string`; `const Invalid, Cancelled Code`; `const TimeLimitMessage`
- `type Position struct { File string; Line, Col int }`; `PositionOf(syntax.Position) Position`
- `type Frame struct { Func string; Pos Position }`
- `NewThread(name string, lim Limits, print func(*starlark.Thread, string)) *starlark.Thread`
- `Run(ctx context.Context, lim Limits, thread *starlark.Thread, fn func() (starlark.Value, error)) (starlark.Value, error)`
- `Call(ctx context.Context, lim Limits, name string, fn starlark.Callable, args starlark.Tuple, print func(*starlark.Thread, string)) (starlark.Value, uint64, error)`
- `Failure(ctx context.Context, filename string, err error, timedOut bool, steps uint64) *Error`
- `IsCancellation(err error, reason string) bool`

`Filename` defaults to `"script.star"`; it names the script in positions and is
the `from` path for relative `load()` specs. `Result` and `Error` carry JSON tags
(`value`, `steps`, `digest`, `modules`; `code`, `message`, `pos`, `backtrace`,
`steps`, `timedOut`).

## Semantics, errors, and limits

### Limits and cancellation

- `Limits.MaxSteps` bounds the program's own Starlark steps (module body plus
  entry function); 0 means unbounded. A module's top level runs on its own
  thread bounded by `modules.Options.MaxSteps`, and those steps are not included
  in `Result.Steps`; functions defined in a module but called by the program run
  on the program's thread, so their steps count and are bounded by
  `Limits.MaxSteps`.
- `Limits.Timeout` is applied as a context deadline on top of the caller's
  context, so it also cancels module loads. 0 means only the caller's context
  bounds the run. `TimedOut` is set only when the deadline's cancellation is
  what stopped the script: a host built-in that returns its own error after the
  deadline reports that error (still reachable via `Unwrap`), not a timeout.
- The zero `Options` has no limits at all; pass `DefaultLimits` or your own.
- Cancellation stops Starlark between steps. Time spent waiting inside a host
  built-in uses no steps and is not interrupted; `Exec` does not pass the context
  to built-ins, so blocking built-ins must observe it themselves.
- A context that is already done cancels the thread before any code runs.

### Errors

Every failure from `Exec` is a `*script.Error`:

| Situation | Code | Notes |
| --- | --- | --- |
| syntax, undefined name, runtime error, host built-in error | `Invalid` | `Pos`, `Backtrace` from Starlark |
| step limit | `Invalid` | message "Starlark computation cancelled: too many steps" |
| `Limits.Timeout` | `Invalid`, `TimedOut: true` | message `TimeLimitMessage` ("script exceeded its time limit") |
| caller's context ended | `Cancelled` | |
| input not JSON-encodable, `Predeclared["input"]` set, result not JSON-convertible | `Invalid` | no position |

- `Pos` is the innermost frame in the program's own file; frames in modules or
  built-ins appear in `Backtrace` (outermost first; built-ins have file
  `<builtin>`). `Error()` formats `code: file:line:col: message`, or
  `code: message` without a position.
- Resolve errors (undefined names) set `Pos` to the first one. A single one's
  `Message` is just its message, so `Error()` reads
  `invalid: bad.star:2:5: undefined: nope`. With several, the first stays bare
  and each later one keeps its own `line:col: `:
  `invalid: bad.star:2:5: undefined: nope; 3:5: undefined: nada`.
- `Unwrap` returns the original cause. For runtime failures that is Starlark's
  `*EvalError`, which unwraps to the error a host built-in returned, so
  `errors.As(err, &hostErr)` works. `*modules.Error`, `*modules.CycleError`,
  `modules.ErrNoSource` and `fs.ErrNotExist` from a failing load are reachable the
  same way. A failing module surfaces at the `load` line of the program, with a
  message such as `cannot load bad.star: bad.star:2:7: floored division by zero`.
- `Failure` is the classifier `Exec` uses: positions from `EvalError`,
  `syntax.Error` or `resolve.ErrorList`; `Cancelled` if `ctx` has ended; otherwise
  `Invalid`, with `TimedOut` and `TimeLimitMessage` when `timedOut` is set. An
  error that is itself a `*script.Error` is returned unchanged; a wrapped one is
  reclassified, with the original reachable via `Unwrap`. Hosts computing
  `timedOut` for `Failure` can use `IsCancellation(err, reason)`, which reports
  whether err is Starlark's cancellation error for a `thread.Cancel` reason.

### Print

`Options.Print` receives each `print()` with the position of the call, including
prints from modules this program's thread loads first. nil discards output.

### Digests

`Result.Modules` lists every module the program loaded, transitively, sorted by
path (an empty, non-nil slice when none), for provenance. `Result.Digest` is
`modules.Digest(Program.Source, deps)`, where `deps` are the program's **direct**
dependencies as `modules.Dep` pairs (each `load()` spec as written, with the
digest of the module it loaded; `Loader.Deps` on the program's thread). See the
[digest formula](modules.md#digest-formula).

- It is the formula a module's `Ref.Digest` uses, so a file has the same digest
  whether it runs as a program or loads as a module.
- Transitive dependencies count through their parents' digests: it changes when
  the program or any module below it changes, including when two dependencies
  swap contents.
- It does not depend on paths: the same files under another directory give the
  same digest.
- It does not cover the input, `Entry`, `Filename`, limits or host built-ins.

### Helpers for other runners

`NewThread` returns a thread bounded by `lim.MaxSteps` whose `print` goes to the
given function (nil discards). `Run` calls `fn`, cancelling the thread when `ctx`
ends or `lim.Timeout` elapses (reason "time limit exceeded"). `Call` combines
them: it calls a Starlark callable on a fresh thread and classifies failures with
`Failure`, positioned in the callable's file and with `TimedOut` set for its own
timeout. [startest](startest.md) is built on these.

## Examples

[`ExampleExec`](../script/example_test.go) is the [README quick
start](../README.md#quick-start): a program loading `./lib/text.star` from a
`modules.Map`, printing through `Options.Print`, returning
`{"greeting":"ABAB"}`, and listing `lib/text.star` and `lib/util.star` in
`Result.Modules`.

[`ExampleError`](../script/example_test.go) shows a host built-in's own error type
surviving the trip through Starlark:

```go
_, err := script.Exec(ctx, script.Program{Filename: "job.star", Source: src, Predeclared: host}, opts)
var se *script.Error
errors.As(err, &se)    // se.Code == script.Invalid, se.Pos.Line == 2
var qe *quotaError
errors.As(err, &qe)    // true: the built-in's error
fmt.Println(err)       // invalid: job.star:2:17: quota exhausted (0 left)
```

[`ExampleLimits`](../script/example_test.go) shows the three ways a runaway loop
ends: `invalid` with "too many steps", `invalid` with `TimedOut` and
"script exceeded its time limit", and `cancelled`.
