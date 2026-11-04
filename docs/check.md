# Check

[Project](../README.md) · [Documentation index](README.md) · [Script](script.md)

Available Go API: `github.com/craggy-io/script-services/check`.

## Purpose

Validate a script without running it: parse and resolve it in the Craggy dialect
against what the host will predeclare, then check every reference that can be seen
statically.

## Features

- Undefined names and syntax errors, from the same [dialect](dialect.md) that
  runs the script.
- Dotted callable paths (`storage.artifact.get(...)`) must name a callable or a
  namespace, with nearest-name suggestions for typos.
- Literal refs passed to ref functions (`call("x.y@2")`, `jobs.start("x.y")`) must
  resolve through a host-supplied resolver.
- Keyword arguments of direct calls checked against the callable's input schema.
- `load()` targets must resolve, exist, parse, and define the loaded names.
- `Analyze` also reports which callables the script uses and which modules it
  loads.

## Mental model

The host describes its world in an `Env`: callables by dotted name with input
schemas, other predeclared names, ref functions, and optionally a module source.
`check` builds a name tree from the callables (`storage.artifact.get` becomes
namespace `storage` → namespace `artifact` → callable `get`) and walks the
script's syntax tree against it. An empty result means the script compiles and
every reference `check` can see resolves; it says nothing about runtime values.

## Responsibilities and boundaries

`check` knows nothing about what callables are. `Callable.Value` is the host's
own payload (a capability registry passes its registry entry), returned untouched
in `Analysis.Uses`, and `Env.Noun` is the word messages use for a callable:
`"callable"` by default, `"capability"` for a capability registry. Hosts must describe the same names they will predeclare at run
time; `check` cannot see built-ins it is not told about.

Checks performed:

- **Names:** the file is resolved with `input`, every `Env.Predeclared` name, and
  the first segment of every callable and ref-function path predeclared. Unknown
  names are `undefined: x`.
- **Paths:** an identifier or attribute chain rooted at a callable namespace must
  resolve: unknown segments are `unknown <noun> a.b.c (did you mean ...?)`;
  selecting through a callable is `a.b is a <noun>, not a namespace: it has no
  attribute "c"` (`is a function` for ref functions); calling a namespace is
  `a.b is a namespace, not a <noun>`. `<noun>` is `Env.Noun`, default
  `callable`.
- **Ref functions:** `Env.RefFuncs` maps a dotted path to a resolver. When a call
  to it has a string literal first argument, the resolver must return a callable
  (`unknown <noun> "x.y"` otherwise, with a suggestion that ignores an `@N`
  version suffix); the remaining arguments are checked against that callable.
  Non-literal refs are not checked.
- **Arguments:** for a direct call (or a resolved ref call) whose arguments are
  all keywords: a nil `Input` with any keyword is `x takes no input`; a closed
  schema (`additionalProperties: false`) rejects unknown names
  (`x has no input "idd" (did you mean id?)`, or `(inputs: a, b)` when nothing is
  close), except names matching a `patternProperties` pattern; every name in
  `Input.Required` must be passed (`x requires input "id"`).
  A positional argument, `*args` or `**kwargs` skips the argument check for that
  call. Argument values and types are not checked.
- **Loads:** each `load()` spec is resolved with `modules.Resolve` against the
  file name and listed once in `Analysis.Loads`. With `Env.Modules` set, the
  target must be readable, parse and resolve against the names modules will see
  at run time (`Env.ModulePredeclared`, defaulting to the program's host names
  except `input`), and each loaded name must be a global of the target (`does not
  define shuot (did you mean shout?)`). Only the direct target is inspected, not
  what it loads. Names with a leading underscore are a resolve error.

Suggestions use edit distance and are offered only when the nearest candidate is
within `max(2, len/3)` edits.

## Public interfaces

- `Check(env Env, filename string, src []byte) []Diagnostic`
- `Analyze(env Env, filename string, src []byte) Analysis`
- `type Env struct { Callables []Callable; Predeclared []string; RefFuncs map[string]func(ref string) (Callable, bool); Modules modules.Source; ModulePredeclared []string; Noun string }`
- `type Callable struct { Name string; Input *jsonschema.Schema; Value any }`
- `type Diagnostic struct { Pos script.Position; Severity, Message string }` (JSON `pos`, `severity`, `message`)
- `type Analysis struct { Diagnostics []Diagnostic; Uses []Callable; Loads []string }`

`filename` defaults to `"script.star"` and is the base for relative `load()` paths.

## Semantics, errors, and limits

- Diagnostics are sorted by file, line and column. A syntax error stops analysis
  and is the only diagnostic.
- `Diagnostic.Severity` is always `"error"` today: every finding means the
  script will not run as written. The field is kept so a later check can add
  warnings without changing the type; treat any other value as non-fatal (the
  CLI exits 1 only for `"error"`).
- `Uses` lists each callable reached by a direct call or a resolved literal ref,
  once per `Name`, in source order. A callable referenced without being called is
  not a use.
- Name-tree conflicts: a ref function replaces a callable of the same path; a
  callable never replaces a namespace, and a path under an existing callable
  (`a` then `a.b`) is dropped.
- `check` takes no context and reads modules with `context.Background()`.
- Messages name callables with `Env.Noun` (`TestNounNamesCallables`), so the
  package carries no host's vocabulary; a capability registry sets
  `Noun: "capability"` to keep its familiar wording.

## Examples

[`ExampleAnalyze`](../check/example_test.go) checks this script against an Env
with one callable (`storage.artifact.get`, input `{id (required), depth}`,
closed), a `log` built-in, a `call` ref function, an in-memory module and
`Noun: "capability"`:

```python
load("./lib/util.star", "twice", "thrice")
a = storage.artifact.get(id = "x")
b = storage.artifact.gte(id = "x")
c = storage.artifact.get(idd = "x")
d = call("storage.artifact.get@2", id = "y")
e = call("storage.artefact.get")
log(twice(input["name"]))
```

```text
1:35 error: load "./lib/util.star": lib/util.star does not define thrice (did you mean twice?)
3:5 error: unknown capability storage.artifact.gte (did you mean storage.artifact.get?)
4:5 error: storage.artifact.get has no input "idd" (did you mean id?)
4:5 error: storage.artifact.get requires input "id"
6:5 error: unknown capability "storage.artefact.get" (did you mean storage.artifact.get?)
uses storage.artifact.get host payload
loads [lib/util.star]
```

The [CLI](cli.md) runs `check` with only `input` predeclared, so scripts that use
host built-ins report them as undefined there; hosts should run `check` with their
own Env.
