# Script Services: coding-agent integration guide

Use this guide when editing this repository or integrating its Go module,
`github.com/craggy-io/script-services`, into a consumer. Repository development
rules below apply to work here. Consumers should explicitly provide this guide to
their coding agent; installing a dependency does not imply its AGENTS.md is read.
Start with the [README quick start](README.md#quick-start) and the executable
package examples (`*/example_test.go`), which `go test` compiles and checks.

## Choose the owning package

| Need | Package and contract |
| --- | --- |
| Parse or compile Starlark source in the shared dialect | [dialect](docs/dialect.md) |
| Convert between Starlark values, Go values and JSON; turn built-in arguments into JSON | [values](docs/values.md) |
| Derive JSON Schema from Go-syntax `types.go` declarations | [types](docs/types.md) |
| Let scripts declare schemas; convert what they declare for the host | [schema](docs/schema.md) |
| Serve `load()`: path resolution, bundle and native sources, one execution per module, cycles, digests | [modules](docs/modules.md) |
| Run a program and its entry function under limits; classify failures | [script](docs/script.md) |
| Validate a script statically against the callables a host will provide | [check](docs/check.md) |
| Compile a bundle's `.star` contribution for any kind; the generic `craggy.script@1` kind | [starcompile](docs/starcompile.md) |
| Run the `*_test.star` files a bundle declares (`craggy.script-test@1`) | [startest](docs/startest.md) |
| Read a bundle's `types.go` (`craggy.types@1`) | [types](docs/types.md) |
| Check, run, test or print schemas of a bundle from a shell | [craggy-script](docs/cli.md) |

`dialect`, `values` and `types` import nothing else from this module. `modules`
builds on `dialect`; `script` on `dialect`, `modules` and `values`; `check` on
`script`; `starcompile` on `check`; `startest` on `starcompile`. `schema` builds
on `types` and `values`. `modules`, `types`, `starcompile`, `startest` and the CLI
import foundation-kit's `bundle` and `kind`, and nothing else from it. Keep host
semantics (what a callable does, who may call it, what it costs) out of all of
them: this module only runs, checks and describes scripts.

## The import rule

This module must never import capability-services, a capability registry, the
computational-research platform, or any other consumer. Dependencies point one
way: consumers import script-services and bridge to it.

- A registry turns its capabilities into Starlark built-ins for
  `script.Program.Predeclared` and into `check.Callable` entries (registry payload
  in `Callable.Value`) for `check.Env`.
- A research platform parses its `types.go` with `types.Parse` and passes
  `schema.WithTypes(pkg)` to scripts and to `schema.Of`.
- Do not add registry vocabulary, ref formats or versioning rules to these
  packages. `check.Env.RefFuncs` exists so a host resolves its own refs, and
  `check.Env.Noun` so it names its callables in messages (a registry passes
  `"capability"`; the default is `"callable"`).

When a consumer needs something new, add a generic seam here (an option, an
interface, a callback) and keep the consumer-specific part in the consumer.

## Load scripts from bundles

Authored scripts (shipped content, user folders, runtime installs and test
fixtures alike) are bundles loaded by foundation-kit's `bundle.Load`, or held in
a `bundle.Live`. This module provides the compilers; it never adds a loader
beside `bundle.Load`.

- **Every folder needs a real `bundle.yaml`.** Never generate or synthesize a
  manifest from a folder layout or file names, in code or in tests: a test
  fixture writes its `bundle.yaml` bytes (an `fstest.MapFS` entry is fine).
  Undeclared files are never discovered as scripts or tests; they are only
  support files that declared scripts `load()`.
- **One compiler.** A domain declares its kind with `kind.New` next to its Go
  type and compiles it with `starcompile.Compiler(opts, build)`. Do not build a
  `modules.NewLoader` over a raw file system in a consumer: `load()` comes from
  `modules.FromContribution` (the bundle, and `@alias/path` from declared
  dependencies), which `starcompile` wires in. Put per-file state (collectors)
  in the built-ins passed to `Unit.Exec`, not in a cache: the compiler must have
  no side effects, because `bundle.Live` reuses compiled values.
- **Generic kinds here:** `starcompile.Script` (`craggy.script@1`),
  `startest.Test` (`craggy.script-test@1`) and `types.Declarations`
  (`craggy.types@1`). Tests are contributions of their own kind, whose paths end
  in `_test.star`.
- **The CLI takes bundles only** (`craggy-script check|run|test|types
  BUNDLE[#ID]`, `-dep DIR` for dependencies). Do not add single-file or
  loose-folder arguments.
- `modules.DirRoot`, `RootSource` and `modules.Dir` have no callers left in this
  module and remain only for consumers that have not moved to bundles yet; do
  not add new uses.

## Construct the pieces

Nothing here runs background goroutines. The one thing to close is a
`*modules.RootSource` from `modules.DirRoot`, which holds its directory open.

1. For bundle content, follow the section above: `starcompile.Compiler` gives
   each script a `*starcompile.Unit`, whose `Modules()` is its module source and
   whose `Loader(predeclared)` and `Exec` cover steps 2 and 3. For a host's own
   in-memory modules, `modules.Map`, or your own `modules.Source` whose `Read`
   wraps `fs.ErrNotExist` for missing modules. Go modules a script loads by
   spec go in `modules.Options.Native` (and `check.Env.Native`).
2. `modules.NewLoader(src, modules.Options{Predeclared: hostNames, MaxSteps: n})`.
   Modules see only the universe plus `Options.Predeclared`, never a program's
   `input`. `MaxSteps` 0 means module top levels are unbounded in steps.
3. `script.Exec(ctx, script.Program{...}, script.Options{Limits: ...})`. The zero
   `Limits` means no step or time bound: pass `script.DefaultLimits` (5,000,000
   steps, 10 minutes) or your own.
4. For static checking, build a `check.Env` from the same host description you use
   for `Predeclared`, then call `check.Check` or `check.Analyze`.
5. For declared types, `types.Parse(filename, src)` once, then
   `schema.Module(schema.WithTypes(pkg))` in `Predeclared` and the same option to
   `schema.Of`.
   In a bundle, a `craggy.types@1` contribution compiled by `types.Compiler`
   gives the `*types.Package`.
6. For Starlark tests, `startest.Run(t, bundleFS, startest.Options{...})` in a Go
   test, where `bundleFS` holds a `bundle.yaml` declaring `craggy.script-test@1`
   contributions.

A Loader is safe for concurrent use and caches each module's frozen globals for
its lifetime, failures included (except context cut-offs). It never re-reads a
source. Share one Loader across runs of an unchanging module tree; construct a new
Loader when sources change.

## Context, limits and cancellation

- Pass a context to `Exec`, `Call`, `Run`, `Loader.Module`, `Loader.Bind`,
  `modules.Address`, `starcompile.Unit.Exec`, `startest.Load` and
  `startest.RunUnit`; nil is treated as `context.Background()`. `check` takes no
  context and reads modules with `context.Background()`.
- `Exec` implements `Limits.Timeout` as a context deadline, so it also bounds
  module loads. `Limits.MaxSteps` bounds only the program's own thread. Each module
  runs on its own thread bounded by `modules.Options.MaxSteps`; its steps are not
  counted in `Result.Steps`.
- Cancellation is cooperative: Starlark threads stop between steps. A host
  built-in blocked in Go is not interrupted, and time spent inside it uses no
  steps. `script` does not hand the context to built-ins; built-ins that block must
  observe the caller's context themselves (for example by closing over it).
- A module load cut short by its context is not cached: a later load runs the
  module again. Other loads waiting on it retry under their own context.
- `startest` applies `Limits` to the file's top level and to each test separately
  (zero means `script.DefaultLimits`). As in `Exec`, the top level's `Timeout`
  is a context deadline, so it also bounds the modules the file loads. See
  [startest](docs/startest.md#semantics-errors-and-limits).

## Errors

Use `errors.As` for structure and `errors.Is` for sentinels.

- `*script.Error` from `Exec`, `Call` and `Failure`: `Code` is `script.Invalid`
  (the script, its input or a limit is at fault) or `script.Cancelled` (the
  caller's context ended). `TimedOut` marks `Limits.Timeout` with the message
  `script.TimeLimitMessage`. `Pos` is the innermost position in the script's own
  file; `Backtrace` is outermost first; `Steps` is what ran. The struct marshals to
  JSON (`code`, `message`, `pos`, `backtrace`, `steps`, `timedOut`).
- `script.Error.Unwrap` returns the original cause; through Starlark's
  `EvalError` that reaches the error a host built-in returned, so
  `errors.As(err, &myHostErr)` works. Return typed errors from built-ins for hosts
  to recover.
- Compiling a bundle contribution reports `bundle.Diagnostic` values (inside a
  `*bundle.LoadError` from `bundle.Load`) with the bundle, contribution, path,
  line and a stable code: `script.source`, `script.check`, `script.load` and
  `script.exec` from `starcompile`, `startest.path`, `types.invalid`, and each
  domain's own codes. Match on the code, not the message.
- `*modules.Error` (`Path`, `Pos`, `Msg`) is a module that failed to read, compile
  or run; `Unwrap` reaches the cause (`fs.ErrNotExist` for a missing module).
  `*modules.CycleError` has `Chain`, starting and ending with the same path.
  `modules.ErrNoSource` is the load failure when `Program.Modules` is nil. All
  three are reachable through a `*script.Error`.
- `*types.Error` (`Pos`, `Msg`) comes from `Package.Schema` for problems inside a
  declaration; `types.Parse` returns Go parser errors.
- `check` returns diagnostics, not errors. Every diagnostic has severity
  `"error"` today; the field exists so warnings can be added later.
- Steps-limit failures are `Invalid` with the message
  "Starlark computation cancelled: too many steps".

## Determinism

`script` exposes no clock, randomness or I/O. Given the same source, input,
modules and host built-ins, a program computes the same result. Keep it that way:

- Do not add time, random numbers, environment or filesystem access to the
  default runtime. Hosts that want them inject them deliberately as
  `Predeclared` built-ins, and accept that such scripts are no longer reproducible
  from their digest alone.
- `values.FromJSON`/`FromGo` build dicts with sorted keys; `Result.Value` is
  marshalled from Go maps, so object keys are sorted.
- `print` is a side channel to `Options.Print`; it does not affect results.

## Digests and provenance

`modules.Digest(src, deps)` is `"sha256:"` plus the hex SHA-256 of the
length-prefixed domain tag `craggy-module-v2`, the length-prefixed source, the
dependency count, and for each `modules.Dep` (sorted by spec) its length-prefixed
`Spec` (the `load()` string exactly as written) and `Digest` (the module it
resolved to). Lengths are big-endian uint64. Paths are not hashed; the specs are
already part of the source. See the [digest formula](docs/modules.md#digest-formula).

- Programs and modules use the same formula over **direct** dependencies only;
  each dependency's digest covers its own in turn. One file has one digest
  whether it runs as a program (`Result.Digest`) or loads as a module
  (`Ref.Digest`).
- Pairing each spec with its digest means swapping the contents of two
  dependencies changes the digest.
- `Result.Modules` still lists **every** module loaded, transitively, for
  provenance; `Loader.Deps(thread)` gives the direct pairs a digest covers.
- Neither covers the input, `Entry`, file names, limits, host built-ins or their
  behaviour, or the go.starlark.net version. Provenance records that need to
  reproduce a run should store `Result.Digest`, `Result.Modules`, the input, and
  the host's own description of its built-ins.
- Equal digests mean equal code and dependencies, wherever the files lived.
  `Ref.Path` is retained for display and lookup only.

## Developing and verifying in this repository

- Preserve concurrent edits and reread before patching. Use `rg` for searches.
- Check existing helpers before adding boilerplate. On the second occurrence of
  a pattern, extract or widen a small shared helper at the narrowest useful scope.
- After changing a data model (for example `script.Error`, `script.Result`,
  `modules.Ref`, `check.Diagnostic`), trace every reader, formatter, JSON tag,
  CLI output and downstream consumer; silent field omissions are bugs.
- Every parse goes through `dialect`. Do not construct other `syntax.FileOptions`
  in an entry point; a script that checks clean must compile when it runs.
- Fully remove obsolete code and call sites rather than adding deprecation markers,
  compatibility shims, or commented-out remnants. Coordinate API removal that
  affects consumers (capability-services, the research platform) with the user.
- Keep examples in `example_test.go` with `// Output:` checks and update the
  guides that quote them in the same change.
- Verify fixes by running the affected code and existing tests. If coverage is
  missing, add and run a minimal regression test or reproduction. Report what ran
  and any limits; do not equate an unrun check with a pass.

From this module root, using the Go version specified in [go.mod](go.mod):

```sh
go test ./... -count=1
go test -race ./...
go vet ./...
go build ./...
go run ./cmd/craggy-script help
```

In a Go workspace (`go.work`) that also contains consumers, run the consumers'
tests after changing an exported API here. Run focused package tests while
iterating and relevant broader checks for the change. This guide lists
verification commands, not a claim that a particular checkout has passed them.
