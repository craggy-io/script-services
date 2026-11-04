# Dialect

[Project](../README.md) · [Documentation index](README.md)

Available Go API: `github.com/craggy-io/script-services/dialect`.

## Purpose

Fix the one Starlark dialect every Craggy script is written in, so a script that
checks clean also compiles when it runs, and a module that loads in one place
loads in every other.

## Features

- One `*syntax.FileOptions` value shared by `script.Exec`, `check`, `modules` and
  `startest`.
- Starlark plus `set()`, `while` loops, top-level `if`/`for`/`while`, and global
  reassignment: what a notebook-style script written top to bottom needs.
- Recursion stays off; step limits bound everything else.
- Thin wrappers for the three ways hosts consume source: Parse, Resolve, Compile.

## Mental model

The dialect is a constant, not a configuration. There is no per-host switch: a
host that wants a different language is not a Craggy host. Entry points differ in
what they predeclare, never in what syntax they accept.

## Responsibilities and boundaries

`dialect` owns syntax and name resolution options only. It does not predeclare
anything beyond the Starlark universe, run code, or apply limits. Callers supply
the predeclared-name predicate.

`dialect.FileOptions` is `{Set: true, While: true, TopLevelControl: true,
GlobalReassign: true}`; `Recursion` is false, so a function that calls itself
fails at run time with "called recursively". Treat `FileOptions` as read-only:
mutating it changes every entry point in the process.

`load` statements are allowed only at top level (standard Starlark).

## Public interfaces

- `var FileOptions *syntax.FileOptions`
- `Parse(filename string, src []byte) (*syntax.File, error)`
- `Resolve(f *syntax.File, isPredeclared func(name string) bool) error`
- `Compile(filename string, src []byte, isPredeclared func(name string) bool) (*starlark.Program, error)`

## Semantics, errors, and limits

- Parse returns `syntax.Error` values carrying their position.
- Resolve resolves names against `isPredeclared` and `starlark.Universe`, and
  reports every unresolved name as a `resolve.ErrorList`. Resolve a file at most
  once.
- Compile parses, resolves and compiles; its errors are either of the above.
- [dialect_test.go](../dialect/dialect_test.go) runs the same programs through
  `check.Check`, `script.Exec`, `modules.Loader.Module` and `startest.RunFile` and
  requires all four to accept or reject each one identically, and verifies that
  recursion is rejected by Exec and by module loading.

## Examples

[`ExampleCompile`](../dialect/example_test.go) compiles and runs a notebook-style
program using a top-level `for`, `while`, `set()` and reassignment:

```go
prog, err := dialect.Compile("notebook.star", src, func(string) bool { return false })
globals, err := prog.Init(&starlark.Thread{}, nil)
fmt.Println(globals["n"]) // 20
```

[`ExampleParse`](../dialect/example_test.go) shows a positioned syntax error:
`2:1 got end of file, want primary expression`.
