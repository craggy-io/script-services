# CLI

[Project](../README.md) · [Documentation index](README.md)

Available command: `github.com/craggy-io/script-services/cmd/craggy-script`.

## Purpose

Check, run and test Craggy Starlark scripts from a shell, and print the JSON
Schemas of Go-syntax type declarations.

## Features

- `check`: static diagnostics, one per line, with load() targets looked up on disk.
- `run`: execute a script with JSON input and print the result, digest and
  modules as JSON.
- `test`: run every `*_test.star` file under a directory.
- `types`: print the schema of one declared type, or of all of them.

## Mental model

The CLI is a thin host over the library with nothing predeclared but `input`
(and `assert` in tests). It is useful for scripts that only use the Starlark
universe and `load()`; scripts written for a host with its own built-ins should be
checked and run by that host.

## Responsibilities and boundaries

Each command composes library packages: `check` uses [check](check.md), `run` uses
[script](script.md) and [modules](modules.md), `test` uses
[startest](startest.md), and `types` uses [types](types.md). The CLI adds file
handling, output formats and exit codes, nothing else.

## Public interfaces

```text
craggy-script check [-root DIR] FILE...
craggy-script run [-input JSON] [-root DIR] FILE
craggy-script test [-root DIR]
craggy-script types FILE.go [NAME]
craggy-script help
```

Install with `go install github.com/craggy-io/script-services/cmd/craggy-script@latest`
or run from this repository with `go run ./cmd/craggy-script`.

| Command | Module root | Output |
| --- | --- | --- |
| `check` | `-root`, default each FILE's directory | `file:line:col: severity: message` per diagnostic, to stdout |
| `run` | `-root`, default FILE's directory | result JSON (`value`, `steps`, `digest`, `modules`) or `{"error": ...}` to stdout; `print()` as `file:line:col: msg` to stderr |
| `test` | `-root`, default `.`; also where test files are found | `PASS`/`FAIL name` lines, indented print output and errors, then `N passed, M failed`; a file's top-level print output comes before its tests and is not counted as a test |
| `types` | n/a | indented JSON Schema, or an object of all schemas keyed by type name |

## Semantics, errors, and limits

- Exit status is 0 on success, 1 when a check reports an error, a run fails or a
  test fails (or a file cannot be read), and 2 on usage errors. `-h` on a command
  prints its flags and exits 0.
- FILE must be inside the module root; it is named by its slash path relative to
  the root in positions (`run` and `check`). Digests do not depend on it.
- Every command reads the module root with
  [`modules.DirRoot`](modules.md#sources): modules are read through an `os.Root`,
  so a symbolic link pointing out of the root does not load (`run` fails, `check`
  reports `cannot read`, a test file loading it fails).
- `run` uses `script.DefaultLimits` and gives modules the same step limit.
  `-input` defaults to `null`; invalid JSON is a usage error.
- `test` uses `startest.Options{}` (so `script.DefaultLimits`) and no extra
  predeclared names.
- `check` predeclares only `input`; any host built-in is reported as undefined.

## Examples

With this tree:

```text
main.star        load('./lib/text.star', 'shout') / print('starting') / def run(input)
bad.star         load('./lib/text.star', 'shuot') / x = nope
lib/text.star    load('./util.star', 'twice') / def shout(s)
lib/util.star    def twice(s)
lib/text_test.star
types.go
```

```console
$ craggy-script run -input '{"who":"ab"}' main.star
main.star:2:6: starting
{
  "value": {
    "greeting": "ABAB"
  },
  "steps": 90,
  "digest": "sha256:b271361b...",
  "modules": [
    {"path": "lib/text.star", "digest": "sha256:e97b605c..."},
    {"path": "lib/util.star", "digest": "sha256:492a8b41..."}
  ]
}

$ craggy-script check main.star bad.star
bad.star:1:26: error: load "./lib/text.star": lib/text.star does not define shuot (did you mean shout?)
bad.star:2:5: error: undefined: nope

$ craggy-script test
PASS lib/text_test.star::test_shout
FAIL lib/text_test.star::test_wrong
    invalid: lib/text_test.star:5:14: assertion failed: "AA" != "a"
1 passed, 1 failed

$ craggy-script types types.go In
{
  "type": "object",
  "properties": { ... },
  "required": ["who"],
  "additionalProperties": false
}
```

A failed run prints the classified error as JSON and exits 1:

```console
$ craggy-script run bad.star
{
  "error": {
    "code": "invalid",
    "message": "undefined: nope",
    "pos": {"file": "bad.star", "line": 2, "col": 5},
    "steps": 0
  }
}
```

The outputs above were captured from the built command (JSON condensed and
digests shortened here). `TestCLI` in [main_test.go](../cmd/craggy-script/main_test.go)
covers each command and exit code.
