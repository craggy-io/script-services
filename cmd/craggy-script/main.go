// Command craggy-script checks, runs and tests the Starlark scripts of a
// bundle, and prints the JSON Schemas of its Go-syntax type declarations.
//
//	craggy-script check [-dep DIR]... BUNDLE[#ID]
//	craggy-script run [-input JSON] [-dep DIR]... BUNDLE[#ID]
//	craggy-script test [-dep DIR]... BUNDLE[#ID]
//	craggy-script types [-dep DIR]... BUNDLE[#ID] [NAME]
//
// BUNDLE is a directory with a bundle.yaml at its root; a folder without one
// is not a bundle, and nothing is ever generated for it. Its contributions
// may be of kind craggy.script@1 (a script), craggy.script-test@1 (a
// *_test.star test file) and craggy.types@1 (a .go declaration file). Every
// command loads the whole bundle through bundle.Load, so any problem in any
// contribution is reported, with file:line positions, and fails the command.
// Each -dep DIR is another bundle loaded beside it, for the dependencies its
// bundle.yaml declares; scripts load their files as "@<dependency-id>/path".
//
// #ID picks one contribution. check prints the problems found (only those of
// ID with #ID). run executes a script (ID, or the bundle's only script) with
// load() served from the bundle and prints the result as JSON, including its
// digest and the modules it loaded. test runs every test file (or ID),
// printing a file's top-level print() output before its tests. types prints
// the schema of NAME, or of every declared type, from a declaration file (ID,
// or the bundle's only one).
//
// Exit status is 0 on success, 1 when a load, check, run or test fails, and
// 2 on usage errors.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/craggy-io/foundation-kit/bundle"
	"github.com/craggy-io/foundation-kit/kind"

	"github.com/craggy-io/script-services/script"
	"github.com/craggy-io/script-services/starcompile"
	"github.com/craggy-io/script-services/startest"
	"github.com/craggy-io/script-services/types"
)

const usage = `usage:
  craggy-script check [-dep DIR]... BUNDLE[#ID]
  craggy-script run [-input JSON] [-dep DIR]... BUNDLE[#ID]
  craggy-script test [-dep DIR]... BUNDLE[#ID]
  craggy-script types [-dep DIR]... BUNDLE[#ID] [NAME]

BUNDLE is a directory containing bundle.yaml; #ID picks one contribution.
`

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	var err error
	code := 0
	switch args[0] {
	case "check":
		code, err = cmdCheck(ctx, args[1:], stdout, stderr)
	case "run":
		code, err = cmdRun(ctx, args[1:], stdout, stderr)
	case "test":
		code, err = cmdTest(ctx, args[1:], stdout, stderr)
	case "types":
		code, err = cmdTypes(ctx, args[1:], stdout, stderr)
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "craggy-script: unknown command %q\n%s", args[0], usage)
		return 2
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(stderr, "craggy-script %s: %v\n", args[0], err)
		if code == 0 {
			code = 1
		}
	}
	return code
}

var errUsage = errors.New("bad usage")

// dirs is a repeatable -dep flag.
type dirs []string

func (d *dirs) String() string     { return strings.Join(*d, ",") }
func (d *dirs) Set(v string) error { *d = append(*d, v); return nil }

func newFlags(name string, stderr io.Writer) (*flag.FlagSet, *dirs) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	deps := &dirs{}
	fs.Var(deps, "dep", "a dependency bundle directory (repeatable)")
	return fs, deps
}

// target is a loaded bundle and the contribution ID the user picked.
type target struct {
	cat  *bundle.Catalog
	main bundle.Identity
	id   string // "" when no #ID was given
}

// splitTarget splits BUNDLE[#ID].
func splitTarget(arg string) (dir, id string, err error) {
	dir, id, found := strings.Cut(arg, "#")
	if dir == "" || (found && id == "") {
		return "", "", fmt.Errorf("%w: want BUNDLE[#ID], got %q", errUsage, arg)
	}
	return dir, id, nil
}

// load loads the bundle in dir with deps. A load failure prints every
// diagnostic (only those of id, when one is given) to out and returns a nil
// target.
func load(ctx context.Context, arg string, deps []string, out io.Writer) (*target, int, error) {
	dir, id, err := splitTarget(arg)
	if err != nil {
		return nil, 2, err
	}
	sources := []bundle.Source{bundle.Dir(dir)}
	for _, d := range deps {
		sources = append(sources, bundle.Dir(d))
	}
	cat, err := bundle.Load(ctx, bundle.Options{
		Sources: sources,
		Compilers: []bundle.AnyCompiler{
			starcompile.Scripts(starcompile.Options{}),
			startest.Compiler(startest.Options{}),
			types.Compiler(),
		},
	})
	if err != nil {
		var le *bundle.LoadError
		if !errors.As(err, &le) {
			return nil, 1, err
		}
		for _, d := range le.Diagnostics {
			if id == "" || d.Contribution == "" || d.Contribution == id {
				fmt.Fprintln(out, formatDiagnostic(d))
			}
		}
		return nil, 1, nil
	}
	t := &target{cat: cat, id: id}
	for _, o := range cat.Owners() {
		if o.Origin == dir {
			t.main = o
		}
	}
	return t, 0, nil
}

// formatDiagnostic renders "bundle: path:line:col: severity: message (code)".
func formatDiagnostic(d bundle.Diagnostic) string {
	var b strings.Builder
	if d.Bundle != "" {
		b.WriteString(d.Bundle + ": ")
	}
	if d.Path != "" {
		b.WriteString(d.Path)
		if d.Line > 0 {
			fmt.Fprintf(&b, ":%d:%d", d.Line, d.Column)
		}
		b.WriteString(": ")
	}
	fmt.Fprintf(&b, "%s: %s (%s)", d.Severity, d.Message, d.Code)
	return b.String()
}

// pick returns the definitions of k in the main bundle: the one named by
// #ID, or all of them, sorted by contribution ID.
func pick[T any](t *target, k *kind.Kind[T]) ([]bundle.Definition[T], error) {
	if t.id != "" {
		def, ok := bundle.Get(t.cat, k, bundle.Ref{Owner: t.main.ID, ID: t.id})
		if !ok {
			return nil, fmt.Errorf("%w: bundle %s has no %s contribution %q", errUsage, t.main.ID, k.ID(), t.id)
		}
		return []bundle.Definition[T]{def}, nil
	}
	var out []bundle.Definition[T]
	for _, def := range bundle.All(t.cat, k) {
		if def.Owner.ID == t.main.ID {
			out = append(out, def)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref.ID < out[j].Ref.ID })
	return out, nil
}

// one is pick for commands that act on a single contribution.
func one[T any](t *target, k *kind.Kind[T]) (bundle.Definition[T], error) {
	defs, err := pick(t, k)
	if err != nil {
		return bundle.Definition[T]{}, err
	}
	if len(defs) != 1 {
		ids := make([]string, len(defs))
		for i, d := range defs {
			ids[i] = d.Ref.ID
		}
		return bundle.Definition[T]{}, fmt.Errorf("%w: bundle %s has %d %s contributions %v: pick one with #ID", errUsage, t.main.ID, len(defs), k.ID(), ids)
	}
	return defs[0], nil
}

func cmdCheck(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs, deps := newFlags("check", stderr)
	if err := fs.Parse(args); err != nil {
		return 2, err
	}
	if fs.NArg() != 1 {
		return 2, fmt.Errorf("%w: want exactly one BUNDLE[#ID]", errUsage)
	}
	t, code, err := load(ctx, fs.Arg(0), *deps, stdout)
	if t == nil {
		return code, err
	}
	if t.id != "" {
		_, a := bundle.Get(t.cat, starcompile.Script, bundle.Ref{Owner: t.main.ID, ID: t.id})
		_, b := bundle.Get(t.cat, startest.Test, bundle.Ref{Owner: t.main.ID, ID: t.id})
		_, c := bundle.Get(t.cat, types.Declarations, bundle.Ref{Owner: t.main.ID, ID: t.id})
		if !a && !b && !c {
			return 2, fmt.Errorf("%w: bundle %s has no contribution %q", errUsage, t.main.ID, t.id)
		}
	}
	return 0, nil
}

func cmdRun(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs, deps := newFlags("run", stderr)
	input := fs.String("input", "null", "JSON input bound to `input`")
	if err := fs.Parse(args); err != nil {
		return 2, err
	}
	if fs.NArg() != 1 {
		return 2, fmt.Errorf("%w: want exactly one BUNDLE[#ID]", errUsage)
	}
	// Keep the input as raw JSON: decoding into any would turn integers
	// beyond float64's exact range (2^53) into different numbers before
	// the script ever sees them.
	var in json.RawMessage
	if err := json.Unmarshal([]byte(*input), &in); err != nil {
		return 2, fmt.Errorf("-input: %w", err)
	}
	t, code, err := load(ctx, fs.Arg(0), *deps, stdout)
	if t == nil {
		return code, err
	}
	def, err := one(t, starcompile.Script)
	if err != nil {
		return 2, err
	}
	u := def.Value
	res, err := script.Exec(ctx, script.Program{Filename: u.Path(), Source: u.Source(), Input: in, Modules: u.Loader(nil)}, script.Options{
		Limits: u.Limits(),
		Print: func(pos script.Position, msg string) {
			fmt.Fprintf(stderr, "%s:%d:%d: %s\n", pos.File, pos.Line, pos.Col, msg)
		},
	})
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err != nil {
		var se *script.Error
		if errors.As(err, &se) {
			_ = enc.Encode(map[string]any{"error": se})
			return 1, nil
		}
		return 1, err
	}
	return 0, enc.Encode(res)
}

func cmdTest(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs, deps := newFlags("test", stderr)
	if err := fs.Parse(args); err != nil {
		return 2, err
	}
	if fs.NArg() != 1 {
		return 2, fmt.Errorf("%w: want exactly one BUNDLE[#ID]", errUsage)
	}
	t, code, err := load(ctx, fs.Arg(0), *deps, stdout)
	if t == nil {
		return code, err
	}
	defs, err := pick(t, startest.Test)
	if err != nil {
		return 2, err
	}
	if len(defs) == 0 {
		return 1, fmt.Errorf("bundle %s declares no %s contributions", t.main.ID, startest.Test.ID())
	}
	passed, failed := 0, 0
	for _, def := range defs {
		for _, o := range startest.RunUnit(ctx, def.Value, startest.Options{}) {
			for _, line := range o.Output {
				fmt.Fprintf(stdout, "    %s\n", line)
			}
			if o.Test == "" && o.Passed() {
				continue // the file's top-level print() output; not a test
			}
			if o.Passed() {
				passed++
				fmt.Fprintf(stdout, "PASS %s\n", o.Name())
				continue
			}
			failed++
			fmt.Fprintf(stdout, "FAIL %s\n    %s\n", o.Name(), o.Err.Error())
			for i := len(o.Err.Backtrace) - 1; i >= 0; i-- {
				fr := o.Err.Backtrace[i]
				fmt.Fprintf(stdout, "      at %s (%s:%d:%d)\n", fr.Func, fr.Pos.File, fr.Pos.Line, fr.Pos.Col)
			}
		}
	}
	fmt.Fprintf(stdout, "%d passed, %d failed\n", passed, failed)
	if failed > 0 {
		return 1, nil
	}
	return 0, nil
}

func cmdTypes(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs, deps := newFlags("types", stderr)
	if err := fs.Parse(args); err != nil {
		return 2, err
	}
	if fs.NArg() < 1 || fs.NArg() > 2 {
		return 2, fmt.Errorf("%w: want BUNDLE[#ID] [NAME]", errUsage)
	}
	t, code, err := load(ctx, fs.Arg(0), *deps, stdout)
	if t == nil {
		return code, err
	}
	def, err := one(t, types.Declarations)
	if err != nil {
		return 2, err
	}
	pkg := def.Value
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if fs.NArg() == 2 {
		s, err := pkg.Schema(fs.Arg(1))
		if err != nil {
			return 1, err
		}
		return 0, enc.Encode(s)
	}
	all := map[string]*jsonschema.Schema{}
	for _, name := range pkg.Names() {
		s, err := pkg.Schema(name)
		if err != nil {
			return 1, err
		}
		all[name] = s
	}
	return 0, enc.Encode(all)
}
