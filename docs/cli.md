# CLI

[Project](../README.md) · [Documentation index](README.md)

Available command: `github.com/craggy-io/script-services/cmd/craggy-script`.

## Purpose

Check, run and test the Starlark scripts of a bundle from a shell, and print the
JSON Schemas of its Go-syntax type declarations.

## Features

- `check`: load the bundle and print every problem, one per line, with file:line
  positions.
- `run`: execute a script with JSON input and print the result, digest and
  modules as JSON.
- `test`: run every test file the bundle declares.
- `types`: print the schema of one declared type, or of all of them.

## Mental model

The CLI is a thin host over `bundle.Load` (foundation-kit) with nothing
predeclared but `input` (and `assert` in tests). It works on **bundles only**: a
directory with a `bundle.yaml` at its root. A single `.star` file or a folder
without a `bundle.yaml` is not accepted, and nothing generates a manifest for it.
Its contributions are read by these kinds:

| Kind | Path | Compiled by |
| --- | --- | --- |
| `craggy.script@1` | a `.star` script | [`starcompile.Scripts`](starcompile.md) |
| `craggy.script-test@1` | a `*_test.star` file | [`startest.Compiler`](startest.md) |
| `craggy.types@1` | a `.go` declaration file | [`types.Compiler`](types.md) |

Other `.star` files in the bundle are support files that scripts `load()`; they
are not run or tested on their own. A bundle with contributions of any other
kind does not load in the CLI (`bundle.kind_unknown`): scripts written for a host
with its own built-ins should be checked and run by that host.

## Responsibilities and boundaries

Each command loads the whole bundle, plus each `-dep` bundle, through
`bundle.Load`, then composes library packages: `run` uses [script](script.md)
with the script's `Unit.Loader`, `test` uses [startest](startest.md), and `types`
uses [types](types.md). The CLI adds argument handling, output formats and exit
codes, nothing else.

## Public interfaces

```text
craggy-script check [-dep DIR]... BUNDLE[#ID]
craggy-script run [-input JSON] [-dep DIR]... BUNDLE[#ID]
craggy-script test [-dep DIR]... BUNDLE[#ID]
craggy-script types [-dep DIR]... BUNDLE[#ID] [NAME]
craggy-script help
```

Install with `go install github.com/craggy-io/script-services/cmd/craggy-script@latest`
or run from this repository with `go run ./cmd/craggy-script`.

`BUNDLE` is the bundle directory. `#ID` picks one contribution by its ID in
`bundle.yaml`. Each `-dep DIR` is another bundle loaded beside it, so that the
dependencies its `bundle.yaml` declares resolve; scripts load a dependency's
files as `@<dependency-id>/path`. Flags come before `BUNDLE`.

| Command | Picks | Output |
| --- | --- | --- |
| `check` | the whole bundle; with `#ID`, only that contribution's problems are printed | `bundle: path:line:col: severity: message (code)` per problem, to stdout; nothing when it loads |
| `run` | `#ID`, or the bundle's only `craggy.script@1` | result JSON (`value`, `steps`, `digest`, `modules`) or `{"error": ...}` to stdout; `print()` as `file:line:col: msg` to stderr |
| `test` | every `craggy.script-test@1`, or `#ID` | `PASS`/`FAIL name` lines, indented print output and errors, then `N passed, M failed`; a file's top-level print output comes before its tests and is not counted as a test |
| `types` | `#ID`, or the bundle's only `craggy.types@1` | indented JSON Schema of `NAME`, or an object of all schemas keyed by type name |

## Semantics, errors, and limits

- Exit status is 0 on success, 1 when the bundle fails to load, a run fails or a
  test fails, and 2 on usage errors (including an unknown `#ID`, or a bundle with
  several scripts and no `#ID`). `-h` on a command prints its flags and exits 0.
- Loading is all-or-nothing: a problem in any contribution, or in a `-dep`
  bundle, fails every command and prints every problem, so fix `check` first.
  Check errors in a script (undefined names, unknown `load()` targets or names)
  are load problems with code `script.check`; a missing or unparsable module
  further down is `script.load`.
- Positions are paths relative to the bundle root. Digests do not depend on them.
- Bundles are read by `bundle.Dir`, which rejects symbolic links
  (`bundle.symlink_forbidden`) and paths that leave the bundle, so a link out of
  the bundle never loads.
- Undeclared files are not discovered: a `*_test.star` file that no
  `craggy.script-test@1` contribution names is not run.
- `run` uses `script.DefaultLimits` and gives modules the same step limit.
  `-input` defaults to `null`; invalid JSON is a usage error.
- `test` uses `startest.Options{}` (so `script.DefaultLimits`) and no extra
  predeclared names. A bundle that declares no test files fails.

## Examples

With these two bundles:

```text
myscripts/
├── bundle.yaml           # see below
├── main.star             load('./lib/text.star', 'shout') / print('starting') / def run(input)
├── bad.star              load('./lib/text.star', 'shuot') / x = nope
├── lib/text.star         load('@example.shared/text/util.star', 'twice') / def shout(s)
├── lib/text_test.star    def test_shout() / def test_wrong()
└── types.go
shared/
├── bundle.yaml           # metadata: {id: example.shared, ...}, contributions: []
└── text/util.star        def twice(s)
```

```yaml
# myscripts/bundle.yaml
apiVersion: craggy.io/bundle/v1
metadata: {id: example.myscripts, version: 1.0.0}
dependencies:
  - id: example.shared
contributions:
  - {id: main, kind: craggy.script@1, path: main.star}
  - {id: text-test, kind: craggy.script-test@1, path: lib/text_test.star}
  - {id: api, kind: craggy.types@1, path: types.go}
```

```console
$ craggy-script run -input '{"who":"ab"}' -dep shared myscripts#main
main.star:2:6: starting
{
  "value": {
    "greeting": "ABAB"
  },
  "steps": 90,
  "digest": "sha256:d3ccc858...",
  "modules": [
    {"path": "@example.shared/text/util.star", "digest": "sha256:492a8b41..."},
    {"path": "lib/text.star", "digest": "sha256:a3a40b8a..."}
  ]
}

$ craggy-script test -dep shared myscripts
PASS lib/text_test.star::test_shout
FAIL lib/text_test.star::test_wrong
    invalid: lib/text_test.star:5:14: assertion failed: "AA" != "a"
      at assert.eq (<builtin>:0:0)
      at test_wrong (lib/text_test.star:5:14)
1 passed, 1 failed

$ craggy-script types -dep shared myscripts In
{
  "type": "object",
  "properties": { ... },
  "required": ["who"],
  "additionalProperties": false
}
```

After adding `{id: bad, kind: craggy.script@1, path: bad.star}` to the manifest,
the bundle no longer loads, and every command prints its problems and exits 1:

```console
$ craggy-script check -dep shared myscripts
example.myscripts: bad.star:1:26: error: load "./lib/text.star": lib/text.star does not define shuot (did you mean shout?) (script.check)
example.myscripts: bad.star:2:5: error: undefined: nope (script.check)
```

A script that checks clean but fails while running prints the classified error
as JSON and exits 1:

```console
$ craggy-script run div#divide        # divide.star: x = 1 / y = x // 0
{
  "error": {
    "code": "invalid",
    "message": "floored division by zero",
    "pos": {"file": "divide.star", "line": 2, "col": 7},
    "backtrace": [{"func": "<toplevel>", "pos": {"file": "divide.star", "line": 2, "col": 7}}],
    "steps": 10
  }
}
```

The outputs above were captured from the built command (JSON condensed and
digests shortened here). `TestCLI` and the other tests in
[main_test.go](../cmd/craggy-script/main_test.go) cover each command and exit
code.
