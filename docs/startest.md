# Startest

[Project](../README.md) · [Documentation index](README.md) · [Script](script.md)

Available Go API: `github.com/craggy-io/script-services/startest`.

## Purpose

Run Starlark tests written in the Craggy dialect, from Go tests or from tools such
as the [CLI](cli.md).

## Features

- Any `*_test.star` file is a test file; every global `test_*` function is a test.
- An `assert` module: `eq`, `ne`, `true`, `false`, `contains`, `fails`.
- Test files may `load()` modules, from the same tree or a separate source.
- Per-test limits (the time limit covering module loads too), positioned
  failures with backtraces, and captured `print()` output attributed to the test
  or to the file's top level that printed it.
- `Run` maps files and tests onto `t.Run` subtests.

## Mental model

A test file is a script whose top level is setup. Its top level runs once; then
each `test_*` function runs, in name order, on a fresh thread with the configured
limits. A test passes when it returns without error. If the top level fails, the
file yields a single failing outcome and no tests run. If it passes but prints,
a passing file-level outcome carrying that output comes before the tests.

## Responsibilities and boundaries

`startest` discovers and runs tests and reports outcomes; it does not provide
fixtures, parallelism or filtering. Host built-ins the scripts need are passed in
`Options.Predeclared`, which both test files and the modules they load see,
alongside `assert`. The package imports Go's `testing` package (for `Run`).

### The `assert` module

| Call | Fails unless |
| --- | --- |
| `assert.eq(got, want, msg = "")` | `got == want` |
| `assert.ne(got, other, msg = "")` | `got != other` |
| `assert.true(cond, msg = "")` | `cond` is truthy |
| `assert.false(cond, msg = "")` | `cond` is falsy |
| `assert.contains(container, item, msg = "")` | `item in container` |
| `assert.fails(fn, substring)` | `fn()` fails with an error containing `substring`; returns that message |

Failures read `assertion failed: [msg: ]...`, for example
`assertion failed: lists: [1, 2] != [1, 3]` or
`assertion failed: lambda failed with "floored division by zero", want an error containing "boom"`.

## Public interfaces

- `Run(t *testing.T, fsys fs.FS, opts Options)`
- `RunFile(ctx context.Context, src modules.Source, p string, opts Options) []Outcome`
- `Files(fsys fs.FS) ([]string, error)`; `IsTestFile(p string) bool`
- `Assert() starlark.Value`
- `type Options struct { Predeclared starlark.StringDict; Limits script.Limits; Modules modules.Source }`
- `type Outcome struct { File, Test string; Err *script.Error; Steps uint64; Output []string }`
  with `Passed() bool` and `Name() string` (`file` or `file::test`)

## Semantics, errors, and limits

- `Files` walks `fsys` and returns every `*_test.star` path, sorted. `Run` fails
  the Go test if there are none; otherwise each file is a subtest and each test a
  nested subtest. Output lines are logged; a failure is reported with its
  backtrace (innermost frame first).
- `Options.Modules` serves `load()`; when nil, modules load from the same source
  as the test files. Paths follow the [modules resolution rules](modules.md#resolution-rules).
- `Options.Limits` bounds the file's top level and each test separately; the zero
  value means `script.DefaultLimits`. `MaxSteps` also bounds each loaded module.
  As in `script.Exec`, the top level's `Timeout` is a context deadline that the
  modules it loads run under too, so a module that never finishes is stopped
  when it runs out: the file's outcome is `Invalid` with `TimedOut` set and the
  message `script.TimeLimitMessage` (`TestTimeoutCoversModuleLoading`). The
  caller's own cancellation is `Cancelled`.
- Failures are `*script.Error` values from `script.Failure`/`script.Call`, so they
  carry `Code` (`Cancelled` when the context ended), `Pos` in the test file and
  `Backtrace`. A missing or unparsable test file, or a failing top level
  (including a failing `load`), yields one outcome with an empty `Test`.
- A `test_*` function that declares parameters fails with
  "test functions take no parameters", positioned at its definition.
- `Output` lines are `file:line: msg`. A test's `Output` holds only what that
  test printed. What the file's top level printed (including prints from the
  modules it loaded) goes to a file-level outcome (empty `Test`): the failing one
  when the top level fails, otherwise a passing one that `RunFile` returns first,
  only when there was output (`TestTopLevelPrintGoesToFileOutcome`). `Run` logs
  it on the file's subtest; the CLI prints it before the file's tests and does
  not count it as a test.

## Examples

From Go tests, with test files under `testdata/`, read through
[`modules.DirRoot`](modules.md#sources) so a symbolic link cannot reach outside
it:

```go
func TestStarlark(t *testing.T) {
	src, err := modules.DirRoot("testdata")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	startest.Run(t, src.FS(), startest.Options{})
}
```

`TestRunPassing` in [startest_test.go](../startest/startest_test.go) does this
with an in-memory `fstest.MapFS` and a host built-in in `Predeclared`.

[`ExampleRunFile`](../startest/example_test.go) runs one file and prints its
outcomes:

```python
load("./text.star", "shout")

def test_shout():
    assert.eq(shout("hi"), "HI!")

def test_wrong():
    assert.eq(shout("hi"), "hi", msg = "shouting")

def test_fails():
    msg = assert.fails(lambda: shout(1), "has no .upper field")
    print(msg)
```

```text
PASS lib/text_test.star::test_fails [lib/text_test.star:11: int has no .upper field or method]
PASS lib/text_test.star::test_shout []
FAIL lib/text_test.star::test_wrong invalid: lib/text_test.star:7:14: assertion failed: shouting: "HI!" != "hi"
```
