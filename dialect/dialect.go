// Package dialect fixes the one Starlark dialect every Craggy script is
// written in. Exec, Check, module loading and the test runner all parse
// through it, so a script that checks clean also compiles when it runs, and a
// module that loads in one place loads in every other.
//
// The dialect is Starlark plus set(), while loops, top-level if/for/while and
// global reassignment: what a notebook-style script written top to bottom
// needs. Recursion stays off; step limits bound everything else.
package dialect

import (
	"go.starlark.net/resolve"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// FileOptions is the Craggy Starlark dialect. Treat it as read-only.
var FileOptions = &syntax.FileOptions{
	Set:             true,
	While:           true,
	TopLevelControl: true,
	GlobalReassign:  true,
}

// Parse parses src as a Starlark file in the Craggy dialect. Syntax errors
// are syntax.Error values carrying their position.
func Parse(filename string, src []byte) (*syntax.File, error) {
	return FileOptions.Parse(filename, src, 0)
}

// Resolve resolves a parsed file's names against the predicate isPredeclared
// and the Starlark universe. It reports every unresolved name as a
// resolve.ErrorList. A file must be resolved at most once.
func Resolve(f *syntax.File, isPredeclared func(name string) bool) error {
	return resolve.File(f, isPredeclared, starlark.Universe.Has)
}

// Compile parses, resolves and compiles src in the dialect against the
// given predeclared names.
func Compile(filename string, src []byte, isPredeclared func(name string) bool) (*starlark.Program, error) {
	_, prog, err := starlark.SourceProgramOptions(FileOptions, filename, src, isPredeclared)
	return prog, err
}
