# Starcompile

[Project](../README.md) · [Documentation index](README.md) · [Modules](modules.md) · [Check](check.md)

Available Go API: `github.com/craggy-io/script-services/starcompile`.

## Purpose

Be the one path from a bundle's `.star` contribution to a checked script that a
domain can use. Bundles are loaded by `bundle.Load` in
[foundation-kit](https://github.com/craggy-io/foundation-kit); this package gives
every kind whose contributions are Starlark files the same compiler, so no domain
builds its own module loader over a file system.

## Features

- One `bundle.Compiler` for any kind: read the file, check it statically, compute
  its digest, then hand a `Unit` to the domain's `build` function.
- `load()` from the contribution's bundle and, as `@alias/path`, from the
  dependencies its `bundle.yaml` declares
  ([`modules.FromContribution`](modules.md#sources)).
- Domain built-ins, per contribution: `Unit.Exec` runs the top level with
  whatever `Predeclared` the domain passes, such as a fresh collector.
- Native Go modules (`Options.Native`), module built-ins
  (`Options.ModulePredeclared`), limits.
- Every problem is a `bundle.Diagnostic` with the file and line.
- The generic kind `craggy.script@1` (`Script`), whose value is the `Unit`.

## Mental model

```text
bundle.yaml contribution ──► Compiler(opts, build)
                               │ read .star from Contribution.Files
                               │ check.Analyze(opts.Check, load() via FromContribution)
                               │ modules.Address (static digest)
                               ▼
                             *Unit ──► build(ctx, u) ──► bundle.Compiled[T]
                                         │ u.Exec(ctx, ExecOptions{Predeclared: domain built-ins})
                                         │ or keep u in T and run it later
```

A folder must have a real `bundle.yaml` to be loaded: nothing here, or in
foundation-kit, generates one from a folder layout or file names.

Two styles of domain use the same compiler:

- **Declarative** (agent definitions, classifiers, data sources, capability
  files): `build` calls `u.Exec` with built-ins that collect declarations, and
  returns what they collected. `Execution.Globals` holds the script's functions
  for later calls.
- **Deferred** (workflows, rules): `build` validates and keeps the `Unit` in the
  domain value; at run time the host calls `u.Exec` with run-time built-ins (and
  `ExecOptions.Thread` for thread locals, `Mutable` for live globals), or
  `script.Exec` with `u.Loader(...)`.

## Responsibilities and boundaries

`starcompile` owns reading, checking, `load()` wiring, digests, top-level
execution and diagnostics for `.star` contributions. Domains own their kinds
(declared with `kind.New` next to their Go types), their built-ins, and what a
script means. The compiler has no side effects: it writes no cache and starts
nothing, and its result depends only on the contribution, its declared
dependencies and the options. `bundle.Live` reuses compiled values across
reloads on that basis, so a domain needs no cache of its own. Built-ins a domain
passes to `Exec` at compile time must keep that true.

## Public interfaces

```go
const (
	CodeSource bundle.Code = "script.source" // path is not a readable .star file
	CodeCheck  bundle.Code = "script.check"  // the static check found a problem
	CodeLoad   bundle.Code = "script.load"   // a loaded module is missing, unparsable or cyclic
	CodeExec   bundle.Code = "script.exec"   // running the top level failed
)

var Script = kind.New[*Unit]("craggy.script", 1)
func Scripts(opts Options) bundle.AnyCompiler

type Options struct {
	Aliases           map[string]string        // @alias -> dependency bundle ID; no entry: the alias is the ID
	Check             check.Env                // Modules and Native are set by the compiler
	ModulePredeclared starlark.StringDict      // built-ins load()ed modules see
	Native            map[string]modules.Native
	Limits            script.Limits            // zero: script.DefaultLimits
}

func Compiler[T any](opts Options, build func(ctx context.Context, u *Unit) (bundle.Compiled[T], []bundle.Diagnostic)) bundle.Compiler[T]
func New(ctx context.Context, c bundle.Contribution, opts Options) (*Unit, []bundle.Diagnostic)

type Unit struct{ /* unexported, immutable */ }
func (u *Unit) Contribution() bundle.Contribution
func (u *Unit) Path() string
func (u *Unit) Source() []byte
func (u *Unit) Modules() modules.Source
func (u *Unit) Ref() modules.Ref
func (u *Unit) Deps() []modules.Dep
func (u *Unit) Analysis() check.Analysis
func (u *Unit) Limits() script.Limits
func (u *Unit) Loader(predeclared starlark.StringDict) *modules.Loader
func (u *Unit) Exec(ctx context.Context, opts ExecOptions) (Execution, error)
func (u *Unit) Diagnostics(err error) []bundle.Diagnostic
func (u *Unit) Errorf(code bundle.Code, pos script.Position, format string, args ...any) bundle.Diagnostic

type ExecOptions struct {
	Predeclared       starlark.StringDict
	ModulePredeclared starlark.StringDict // nil: Options.ModulePredeclared
	Thread            *starlark.Thread    // nil: a new thread bounded by Limits().MaxSteps
	Print             func(pos script.Position, msg string)
	Mutable           bool                // leave globals unfrozen
}

type Execution struct {
	Globals starlark.StringDict
	Modules []modules.Ref
	Steps   uint64
}
```

## Semantics, errors, and limits

- The contribution's path must end in `.star` (`script.source`).
- The check runs `check.Analyze` with `opts.Check`, the contribution's module
  source and `opts.Native`. When `opts.Check.ModulePredeclared` is nil it is set
  to the names in `opts.ModulePredeclared`; set it yourself when modules see
  different built-ins at run time (`ExecOptions.ModulePredeclared`). Check errors
  are `script.check` errors and stop the compile before `build`; other check
  diagnostics become warnings added to `build`'s result.
- `Unit.Ref().Digest` is `modules.Digest` over the source and its direct
  dependencies, computed by `modules.Address`; it equals `script.Exec`'s
  `Result.Digest` for the same file and modules. `script.load` reports a module
  that `Address` cannot reach.
- `Exec` runs the top level once under `Limits()`, as `script.Exec` does: the
  timeout is a context deadline that also bounds module loads, and the step limit
  bounds the top level and each module. Its errors are `*script.Error`.
  `ExecOptions.Thread` replaces the thread (Exec sets its `Load`). Globals are
  frozen unless `Mutable`.
- `Diagnostics(err)` turns an `Exec`, `script.Exec` or `script.Call` error into one
  `script.exec` diagnostic placed at the innermost frame in the script, or in the
  failing module of this bundle when a load failed. A position in a dependency's
  module (`@alias/...`) is not a file of this bundle, so the diagnostic is placed
  on the script and its message starts with the module's position.
- `Errorf` is for the domain's own problems with a script that ran; namespace its
  codes by the domain's kind.
- A `Unit` is immutable and safe for concurrent use. Each `Exec` and `Loader`
  call builds a fresh `modules.Loader`, so modules run again per call.

## Examples

[`ExampleCompiler`](../starcompile/example_test.go) compiles a domain kind whose
files call a `team()` built-in, loading names from a dependency bundle:

```go
var TeamKind = kind.New[Team]("example.team", 1)

func TeamCompiler() bundle.AnyCompiler {
	opts := starcompile.Options{Check: check.Env{Predeclared: []string{"team"}}}
	return bundle.Compile(TeamKind, starcompile.Compiler(opts, func(ctx context.Context, u *starcompile.Unit) (bundle.Compiled[Team], []bundle.Diagnostic) {
		var t Team
		team := starlark.NewBuiltin("team", func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
			for _, a := range args {
				s, _ := starlark.AsString(a)
				t.Members = append(t.Members, s)
			}
			return starlark.None, nil
		})
		if _, err := u.Exec(ctx, starcompile.ExecOptions{Predeclared: starlark.StringDict{"team": team}}); err != nil {
			return bundle.Compiled[Team]{}, u.Diagnostics(err)
		}
		return bundle.Compiled[Team]{Value: t}, nil
	}))
}
```

```yaml
apiVersion: craggy.io/bundle/v1
metadata: {id: example.app, version: 1.0.0}
dependencies:
  - id: example.names
contributions:
  - {id: reviewers, kind: example.team@1, path: teams/reviewers.star}
```

```python
# teams/reviewers.star
load('@example.names/names.star', 'ADA', 'GRACE')
team(ADA, GRACE)
```

```text
example.app#reviewers [ada grace]
```

[`ExampleScripts`](../starcompile/example_test.go) loads a `craggy.script@1`
contribution and runs its top level later.
