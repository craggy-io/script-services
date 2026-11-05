# Modules

[Project](../README.md) · [Documentation index](README.md) · [Script](script.md)

Available Go API: `github.com/craggy-io/script-services/modules`.

## Purpose

Implement `load()` for Craggy scripts: resolve module paths, read module source,
execute each module once, and give every result a content address.

## Features

- Five spellings of a load path (`./`, `../`, `//`, bare, and `@alias/` for a
  dependency bundle), all canonicalised and confined to their tree's root.
- Sources: `FromContribution`, the one Source for scripts loaded from bundles
  (its own bundle's files, and declared dependencies under `@alias/`); the
  primitives `Map` (in memory), `Dir` (any `fs.FS`) and `DirRoot` (a directory
  through `os.Root`), which remain only until their last consumers move to
  bundles.
- Native modules: Go values a `load()` names by exact spec (`Options.Native`),
  with a versioned digest.
- `Address`: the digest a run would report, computed statically.
- One execution per module per Loader; concurrent loads of one path share it.
- Frozen, cached globals; deterministic failures cached too; context cut-offs not.
- Cycle detection that names the chain, including cycles entered concurrently
  from both ends.
- Content digests over the source and each `load()` spec paired with the digest
  of the module it loaded, independent of paths; one formula for programs and
  modules.

## Mental model

A Loader is a cache keyed by canonical path over one Source. The first load of a
path reads and executes the module on its own thread; every later or concurrent
load of that path gets the same frozen globals, `Ref` and error. A module's
identity for provenance is its digest, not its path: identical code with identical
dependencies has one digest wherever it lives, a change anywhere below a module
changes its digest, and a file has the same digest whether `script.Exec` runs it
as a program or a Loader loads it as a module.

## Responsibilities and boundaries

`modules` owns path rules, source reading, module execution and digests. It does
not decide what modules may touch: the host passes `Options.Predeclared`, which
every module sees in addition to the Starlark universe. A program's own globals
(such as `input`) are never visible to modules. Hosts wire a Loader into a run
with `script.Program.Modules`, into static checks with `check.Env.Modules` (as a
Source), and into tests with `startest.Options.Modules`.

### Resolution rules

`Resolve(from, spec)` turns a load spec into a canonical path: slash-separated,
relative to the module root, cleaned, ending in `.star`. `from` is the loading
file's canonical path.

There are several trees: the main tree (the script's own bundle) and one tree
per dependency, whose canonical paths start with `@alias/`. A file's `//` and
bare specs resolve against the root of its own tree, and `..` cannot leave it.

| Spec | Resolves against | `from = "lib/text.star"` | `from = "@lib/text/up.star"` |
| --- | --- | --- | --- |
| `./x.star` | the loading file's directory | `./util.star` → `lib/util.star` | `./b.star` → `@lib/text/b.star` |
| `../x.star` | the loading file's directory | `../top.star` → `top.star` | `../b.star` → `@lib/b.star` |
| `//x.star` | its tree's root | `//lib/x.star` → `lib/x.star` | `//b.star` → `@lib/b.star` |
| `x.star` (bare) | its tree's root | `lib/x.star` → `lib/x.star` | `b.star` → `@lib/b.star` |
| `@alias/x.star` | the root of dependency `alias` | `@lib/x.star` → `@lib/x.star` | the same |

`@alias` is an alias for a dependency bundle (see `FromContribution`): letters,
digits, `.`, `_` and `-`, starting and ending with a letter or digit, so a
bundle ID such as `example.shared-lib` is a valid alias.

Rejected specs:

- empty (`empty module path`), or containing `\` or NUL (`bad module path`);
- starting with a single `/` (`module path "/etc/x.star" is absolute: use //etc/x.star for a root path`);
- escaping the root after cleaning, including `//` alone (`escapes the module root`);
- `@` without `alias/path`, or with an invalid alias (`must be @alias/path`);
- a path in a tree that starts with `@`, such as `//@lib/x.star` (`names a dependency without @`);
- not naming a `.star` file (`must name a .star file`).

A top-level program passes its own file name as `from`; `script.Exec` uses
`Program.Filename`, so `./` specs resolve next to the program.

### Sources

- `FromContribution(c bundle.Contribution, aliases map[string]string)` is the
  Source for a `.star` contribution of a foundation-kit bundle, and the one that
  [starcompile](starcompile.md) uses for every kind. It serves main-tree paths
  from `c.Files` (the bundle's snapshot) and `@alias/x.star` from
  `c.Dependency(aliases[alias])`, or `c.Dependency(alias)` when `aliases` has no
  entry, so a dependency's ID is its default alias. The dependency must be
  declared under `dependencies` in `bundle.yaml` and loaded; otherwise the read
  fails with an error naming the alias (not `fs.ErrNotExist`). A dependency's own
  `@alias` loads use the same aliases. Nothing is looked up across several file
  systems in order.
- `DirRoot(dir)` is the Source for a directory on disk. It opens `dir` as an
  `os.Root`, so a path, `..` or symbolic link that would leave the directory
  fails to read (a symbolic link that stays inside works). It returns a
  `*RootSource`, which holds the directory open: `Close` it when nothing reads
  through it any more; reads after `Close` fail. `FS()` returns the same
  confined view as an `fs.FS`. Nothing in this module uses it any more; it stays
  until capability-services and agent-sdk load their scripts from bundles.
- `Dir(fsys)` reads any `fs.FS`. Use it for an `embed.FS`, an `fstest.MapFS` or
  another file system that cannot escape. Avoid `Dir(os.DirFS(dir))`: `os.DirFS`
  follows symbolic links out of `dir` (`TestDirRootRejectsSymlinkEscape` shows
  both behaviours).
- `Map(files)` serves modules from memory, for tests of the Loader itself and of
  hosts.

### Native modules

`Options.Native` maps an exact `load()` spec, such as `"@host/http"`, to a
`Native{Members, Version}`. The spec is matched before path resolution, in
programs and modules alike, and binds names from `Members`, which `NewLoader`
freezes. Its digest is `NativeDigest(spec, version)`: a SHA-256 under the domain
tag `craggy-native-v1` over the length-prefixed spec and version. Bump the
version whenever what the members do changes, because their code is not hashed.
`check.Env.Native` checks loads of the same specs statically.

### Static addresses

`Address(ctx, src, opts, path, code)` returns the `Ref` and direct `Dep`s that a
Loader would give the file after running it, without running anything: it
parses the file and every module it loads, transitively, and applies `Digest`
bottom-up. Because `load()` statements are all at top level, this equals what a
run reports. A missing, unparsable or cyclic module is a `*modules.Error` or
`*CycleError` naming it. [starcompile](starcompile.md) uses it to give every
compiled script its digest.

### Execution, caching and coalescing

- Each module runs at most once per Loader, in the [dialect](dialect.md), with
  `Options.Predeclared`, on a child thread bounded by `Options.MaxSteps` (0 means
  unbounded) and cancelled when the load's context ends.
- Its globals are frozen after execution and cached for the Loader's lifetime.
  Concurrent loads of one path wait for the single execution and share its result
  (`TestConcurrentLoadsCoalesce`: eight concurrent loads, one read, one run).
- Deterministic failures (missing file, syntax error, runtime error, step limit,
  cycle) are cached like successes.
- A load whose execution ended with an error while its context was done is not
  cached: a later load runs the module again, and loads that were waiting on it
  retry under their own contexts.
- A module's `print()` goes to the loading thread's `Print` when the first load
  comes through a bound thread; loads through `Loader.Module` discard it.
- A Loader never re-reads a path. Construct a new Loader when sources change.

### Cycle detection

The Loader records which module each executing module is waiting on. A load that
would wait on a module that is (transitively) waiting on it fails immediately with
a `*CycleError` whose `Chain` starts and ends with the same path, for example
`load cycle: a.star -> b.star -> c.star -> a.star` or `self.star -> self.star`.
Because waits are tracked across goroutines, two loads entering a cycle from
opposite ends both fail instead of deadlocking
(`TestConcurrentCycleDoesNotDeadlock`).

### Digest formula

`Digest(src, deps)` returns `"sha256:"` followed by the hex SHA-256 of:

```text
uint64be(len("craggy-module-v2")) || "craggy-module-v2"
uint64be(len(src)) || src
uint64be(len(deps))
for each Dep d, sorted by d.Spec (then d.Digest):
    uint64be(len(d.Spec)) || d.Spec
    uint64be(len(d.Digest)) || d.Digest
```

A `Dep` is one direct dependency: `Spec` is the `load()` string exactly as
written in the source (`"./util.star"`, not the resolved `lib/util.star`) and
`Digest` is the digest of the module it resolved to. The Loader passes one `Dep`
per distinct spec a file loads.

- **Which module stands behind which `load()` is covered.** Swapping the contents
  of two dependencies loaded under different specs changes the digest
  (`TestDigestSwapCollision`); a set of dependency digests alone would not.
- **Direct dependencies only.** Each dependency's digest covers its own
  dependencies in turn, so a change anywhere below a file changes its digest.
- **Paths are not hashed.** Specs are already part of the source, so hashing them
  again keeps the digest path-independent: the same files under another
  directory get the same digests. Dependency order does not matter.
- **Programs and modules agree.** `script.Exec` computes `Result.Digest` with the
  same formula over the program's direct dependencies (`Loader.Deps`), so a
  file has one digest whether it runs as a program or loads as a module
  (`TestProgramAndModuleDigestsAgree`; see
  [script digests](script.md#semantics-errors-and-limits)).

## Public interfaces

- `type Source interface { Read(ctx context.Context, path string) ([]byte, error) }`
- `FromContribution(c bundle.Contribution, aliases map[string]string) Source`
- `Dir(fsys fs.FS) Source`, `Map(files map[string]string) Source`
- `DirRoot(dir string) (*RootSource, error)`; `*RootSource` implements `Source`
  and has `FS() fs.FS` and `Close() error`
- `Resolve(from, spec string) (string, error)`
- `Digest(src []byte, deps []Dep) string`
- `Address(ctx context.Context, src Source, opts Options, p string, code []byte) (Ref, []Dep, error)`
- `type Native struct { Members starlark.StringDict; Version string }`; `NativeDigest(spec, version string) string`
- `type Dep struct { Spec, Digest string }` (JSON `spec`, `digest`)
- `type Ref struct { Path, Digest string }` (JSON `path`, `digest`)
- `NewLoader(src Source, opts Options) *Loader`;
  `type Options struct { Predeclared starlark.StringDict; MaxSteps uint64; Native map[string]Native }`
- `(*Loader).Bind(ctx context.Context, thread *starlark.Thread, from string)`
- `(*Loader).Module(ctx context.Context, p string) (starlark.StringDict, Ref, error)`
- `(*Loader).Refs(thread *starlark.Thread) []Ref`
- `(*Loader).Deps(thread *starlark.Thread) []Dep`
- `type Error struct { Path string; Pos syntax.Position; Msg string }` with `Unwrap`
- `type CycleError struct { Chain []string }`
- `var ErrNoSource error`

## Semantics, errors, and limits

- `Source.Read` receives canonical paths. `Dir` and `DirRoot` reject paths that
  are not `fs.ValidPath` and return the file system's errors (for `DirRoot`, also
  an error for any path or symbolic link leaving the root); `Map` cleans its keys
  (stripping a leading `/`) and returns an `*fs.PathError` wrapping
  `fs.ErrNotExist` for a missing path. All check the context first.
- `Bind` installs `thread.Load` so `load()` on that thread goes through the Loader,
  resolving specs against `from`, under `ctx`. `Refs(thread)` then returns every
  module loaded directly or transitively by programs on that thread, sorted by
  path: the provenance list. `Deps(thread)` returns only the direct
  dependencies as `(spec, digest)` pairs, sorted by spec: what the program's
  digest covers.
- `Module` requires an already-canonical path (`@alias/x.star` included);
  `./lib/x.star` fails with `not canonical (want "lib/x.star")`.
- A failed module is a `*modules.Error` formatted `path:line:col: message` (or
  `path: message` without a position). `Pos` is the innermost position in the
  failing module itself. `Unwrap` returns the cause, so `errors.Is(err,
  fs.ErrNotExist)` identifies a missing module and `errors.As` finds a
  `*CycleError` or a host built-in's error. A module that fails while loading a
  failing dependency nests the message:
  `user.star:1:1: cannot load div.star: div.star:2:14: floored division by zero`.
  Resolve errors (undefined names) give the first one's position once:
  `m.star:1:5: undefined: nope`, and with several,
  `m.star:1:5: undefined: nope; 2:5: undefined: nada`.
- Step-limit failures read `Starlark computation cancelled: too many steps`;
  cancellations read `Starlark computation cancelled: context done: ...`.
- `ErrNoSource` is what `script.Exec` returns from `load()` when the program has no
  Loader. `Loader.Bind` is the only way this package installs `thread.Load`.

## Examples

From [example_test.go](../modules/example_test.go):

- `ExampleResolve` prints the resolution table above, including the escape and
  absolute-path errors.
- `ExampleLoader_Module` loads `lib/text.star`, calls its `shout`, and shows that
  the same files under `copy/deep/` have the same digest, and that the digest
  equals `modules.Digest(source, []modules.Dep{{Spec: "./util.star", Digest:
  utilRef.Digest}})`.
- `ExampleLoader_Bind` binds a host thread, runs a program that loads
  `./lib/text.star`, and lists `Refs(thread)` (`lib/text.star`,
  `lib/util.star`) and `Deps(thread)` (`./lib/text.star` only).
- `ExampleDirRoot` loads a module from a directory with `DirRoot` and shows that
  a symbolic link pointing out of it does not load.
- `ExampleCycleError` recovers both the `*CycleError` (`[a.star b.star a.star]`)
  and the `*modules.Error` of the module that hit it.

```go
src, err := modules.DirRoot(root)
if err != nil {
	return err
}
defer src.Close()
l := modules.NewLoader(src, modules.Options{
	Predeclared: starlark.StringDict{"schema": schema.Module()},
	MaxSteps:    script.DefaultLimits.MaxSteps,
})
globals, ref, err := l.Module(ctx, "lib/text.star")
```
