# Startest

[Project](../README.md) · [Documentation index](README.md) · [Script](script.md)

Available Go API: `github.com/craggy-io/script-services/startest`.

## Purpose

Run Starlark tests written in the Craggy dialect, from Go tests or from tools such
as the [CLI](cli.md).

## Features

- A test file is a contribution of kind `craggy.script-test@1` in a bundle's
  `bundle.yaml`, whose path ends in `_test.star`; every global `test_*` function
  in it is a test. Undeclared files are not discovered.
- An `assert` module: `eq`, `ne`, `true`, `false`, `contains`, `fails`.
- Test files may `load()` modules of their bundle, and of a declared dependency
  bundle as `@<dependency-id>/path` (or an alias from `Options.Aliases`).
- Per-test limits (the time limit covering module loads too), positioned
  failures with backtraces, and captured `print()` output attributed to the test
  or to the file's top level that printed it.
- `Run` loads a bundle and maps its test files and tests onto `t.Run` subtests.

## Mental model

Tests are contributions of their own kind, so the manifest says which files are
tests: `startest.Compiler` compiles them through [starcompile](starcompile.md)
(checked with `assert` and `Options.Predeclared`), and `RunUnit` runs one. A
folder needs a real `bundle.yaml` to be tested; nothing generates one.

```yaml
apiVersion: craggy.io/bundle/v1
metadata: {id: example.text, version: 1.0.0}
contributions:
  - {id: text-test, kind: craggy.script-test@1, path: lib/text_test.star}
```

A test file is a script whose top level is setup. Its top level runs once; then
each `test_*` function runs, in name order, on a fresh thread with the configured
limits. A test passes when it returns without error. If the top level fails, the
file yields a single failing outcome and no tests run. If it passes but prints,
a passing file-level outcome carrying that output comes before the tests. A file
defining no `test_*` functions yields a single failing file-level outcome, so a
renamed or mistyped test cannot silently pass; a `test_*` name bound to a
non-function likewise fails, while one bound to a host built-in runs as a test.

## Responsibilities and boundaries

`startest` loads the tests a bundle declares, runs them and reports outcomes; it does not provide
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

- `var Test = kind.New[*starcompile.Unit]("craggy.script-test", 1)`; `const CodePath bundle.Code = "startest.path"`
- `Compiler(opts Options) bundle.AnyCompiler`
- `Load(ctx context.Context, fsys fs.FS, opts Options) ([]bundle.Definition[*starcompile.Unit], error)`
- `Run(t *testing.T, fsys fs.FS, opts Options)`
- `RunUnit(ctx context.Context, u *starcompile.Unit, opts Options) []Outcome`
- `IsTestFile(p string) bool`
- `Assert() starlark.Value`
- `type Options struct { Predeclared starlark.StringDict; Limits script.Limits; Aliases map[string]string; Native map[string]modules.Native; Dependencies []bundle.Source; Compilers []bundle.AnyCompiler }`
- `type Outcome struct { File, Test string; Err *script.Error; Steps uint64; Output []string }`
  with `Passed() bool` and `Name() string` (`file` or `file::test`)

## Semantics, errors, and limits

- `Load` loads the bundle rooted at `fsys` (which must hold `bundle.yaml`) with
  `Options.Dependencies`, compiling `Test` and `Options.Compilers`, and returns
  the bundle's `Test` definitions sorted by path. It fails with the
  `*bundle.LoadError` when any contribution has a problem, and when the bundle
  declares no test files. `Run` fails the Go test with every diagnostic in that
  case; otherwise each file is a subtest and each test a nested subtest. Output
  lines are logged; a failure is reported with its backtrace (innermost frame
  first).
- A `Test` contribution whose path does not end in `_test.star` is the
  diagnostic `startest.path`. Problems a static check finds (a syntax error, an
  undefined name, a `load()` of a missing module or name) are load diagnostics
  (`script.check`) with positions, not test outcomes.
- `load()` is served from the bundle and its declared dependencies
  ([`modules.FromContribution`](modules.md#sources)); paths follow the
  [modules resolution rules](modules.md#resolution-rules).
- `Options.Limits` bounds the file's top level and each test separately; the zero
  value means `script.DefaultLimits`. `MaxSteps` also bounds each loaded module.
  As in `script.Exec`, the top level's `Timeout` is a context deadline that the
  modules it loads run under too, so a module that never finishes is stopped
  when it runs out: the file's outcome is `Invalid` with `TimedOut` set and the
  message `script.TimeLimitMessage` (`TestTimeoutCoversModuleLoading`). The
  caller's own cancellation is `Cancelled`.
- Failures are `*script.Error` values from `script.Failure`/`script.Call`, so they
  carry `Code` (`Cancelled` when the context ended), `Pos` in the test file and
  `Backtrace`. A failing top level (including a module that fails while it
  runs) yields one outcome with an empty `Test`.
- A `test_*` function that declares parameters fails with
  "test functions take no parameters", positioned at its definition.
- `Output` lines are `file:line: msg`. A test's `Output` holds only what that
  test printed. What the file's top level printed (including prints from the
  modules it loaded) goes to a file-level outcome (empty `Test`): the failing one
  when the top level fails, otherwise a passing one that `RunUnit` returns first,
  only when there was output (`TestTopLevelPrintGoesToFileOutcome`). `Run` logs
  it on the file's subtest; the CLI prints it before the file's tests and does
  not count it as a test.

## Examples

From Go tests, with a bundle under `testdata/scripts` (its `bundle.yaml`
declares the test files), read through an `os.Root` so a symbolic link cannot
reach outside it:

```go
func TestStarlark(t *testing.T) {
	root, err := os.OpenRoot("testdata/scripts")
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	startest.Run(t, root.FS(), startest.Options{})
}
```

`TestRunPassing` in [startest_test.go](../startest/startest_test.go) does this
with an in-memory `fstest.MapFS` that holds a written `bundle.yaml`, and a host
built-in in `Predeclared`.

[`ExampleRunUnit`](../startest/example_test.go) loads a bundle, runs its one test
file and prints its outcomes:

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
