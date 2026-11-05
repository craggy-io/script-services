// Package starcompile is the one path from a bundle's .star contribution to
// a checked, loadable script. Bundles are loaded by package bundle of
// foundation-kit; this package gives every kind whose contributions are
// Starlark files the same compiler, so no domain builds its own module
// loader over a file system.
//
// # Mental model
//
//   - A domain declares its kind with kind.New next to its Go type, and
//     builds its compiler with Compiler(opts, build).
//   - For each contribution, the compiler reads the .star file from the
//     bundle, checks it statically (package check) against opts.Check, and
//     computes its content address without running it (modules.Address).
//     load() is served by modules.FromContribution: the bundle's own files,
//     and "@alias/path" from a dependency declared in bundle.yaml.
//   - build receives the checked Unit. A declarative domain runs the top
//     level with Unit.Exec and its own built-ins, often a fresh collector, and
//     returns what they collected. A domain that runs scripts later keeps
//     the Unit in its value and calls Exec, Loader or script.Exec at run
//     time.
//   - Every problem is a bundle.Diagnostic with the file and line: CodeSource,
//     CodeCheck, CodeLoad and CodeExec from here, the domain's own codes from
//     build (Unit.Errorf).
//   - Script (craggy.script@1) is the generic kind, compiled by Scripts, whose
//     value is the Unit itself. The craggy-script CLI uses it.
//
// # Invariants
//
//   - A folder needs a real bundle.yaml to be loaded; nothing here generates
//     one.
//   - The compiler has no side effects: it writes no cache and starts
//     nothing. Its result depends only on the contribution, its declared
//     dependencies and opts, so bundle.Live can reuse it across reloads and a
//     domain needs no cache of its own.
//   - A Unit's digest is the digest script.Exec reports for the same file
//     (Result.Digest), because both use modules.Digest over the same direct
//     dependencies.
//   - Nothing is looked up across several file systems in order.
//
// # Concurrency
//
// bundle.Load runs compilers concurrently; each call builds its own Unit. A
// Unit is immutable and safe for concurrent use: each Exec and Loader call
// gets a fresh modules.Loader.
//
// # Exported names
//
//	Compiler, New                 compile a .star contribution into a Unit
//	Options                       aliases, check environment, module built-ins, natives, limits
//	Unit                          a checked script: Path, Source, Modules, Ref, Deps, Analysis, Limits
//	Unit.Exec, ExecOptions, Execution  run the top level
//	Unit.Loader                   a Loader for running it another way
//	Unit.Diagnostics, Unit.Errorf turn failures into bundle diagnostics
//	Script, Scripts               the generic craggy.script@1 kind and its compiler
//	CodeSource, CodeCheck, CodeLoad, CodeExec  diagnostic codes
package starcompile
