// Package startest runs Starlark tests written in the Craggy dialect.
//
// A test file is a contribution of kind craggy.script-test@1 (Test) in a
// bundle's bundle.yaml, whose path ends in "_test.star". Only declared
// contributions run: an undeclared *_test.star file is not discovered. Its
// top level runs once; then every global function named test_* runs, in
// name order, on a fresh thread with the configured limits. A test passes
// when it returns without error. Test files may load() modules of their
// bundle and, as "@alias/x.star", of the bundles it declares as
// dependencies, and see an `assert` module:
//
//	assert.eq(got, want, msg = "")       # got == want
//	assert.ne(got, other, msg = "")      # got != other
//	assert.true(cond, msg = "")          # cond is truthy
//	assert.false(cond, msg = "")         # cond is falsy
//	assert.contains(container, item, msg = "")
//	assert.fails(fn, substring)          # fn() fails with an error containing substring;
//	                                     # returns the error message
//
// From Go tests, Run loads a bundle and registers each test file and test
// as t.Run subtests:
//
//	//go:embed testdata/scripts
//	var scripts embed.FS
//
//	func TestStarlark(t *testing.T) {
//		root, _ := fs.Sub(scripts, "testdata/scripts") // holds bundle.yaml
//		startest.Run(t, root, startest.Options{})
//	}
//
// Limits bound the file's top level (including the modules it loads) and each
// test separately. print() output goes to the outcome of the test that
// printed; output from the file's top level goes to a file-level outcome.
package startest

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"testing"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
	"go.starlark.net/syntax"

	"github.com/craggy-io/foundation-kit/bundle"
	"github.com/craggy-io/foundation-kit/kind"

	"github.com/craggy-io/script-services/check"
	"github.com/craggy-io/script-services/dialect"
	"github.com/craggy-io/script-services/modules"
	"github.com/craggy-io/script-services/script"
	"github.com/craggy-io/script-services/starcompile"
)

// Test is the kind of a Starlark test file, craggy.script-test@1. Its value
// is the checked file, which RunUnit runs.
var Test = kind.New[*starcompile.Unit]("craggy.script-test", 1)

// CodePath is the diagnostic for a Test contribution whose path does not end
// in "_test.star".
const CodePath bundle.Code = "startest.path"

// Options configure a test run.
type Options struct {
	// Predeclared are host names test files and the modules they load see,
	// besides assert.
	Predeclared starlark.StringDict
	// Limits bound the file's top level and each test separately. Zero
	// means script.DefaultLimits.
	Limits script.Limits
	// Aliases and Native are as in starcompile.Options.
	Aliases map[string]string
	Native  map[string]modules.Native
	// Dependencies are the bundles Run loads beside the one under test, so
	// that its declared dependencies resolve.
	Dependencies []bundle.Source
	// Compilers are the other kinds Run must compile, when the bundle under
	// test or a dependency also holds contributions of other kinds.
	Compilers []bundle.AnyCompiler
}

func (o Options) predeclared() starlark.StringDict {
	out := starlark.StringDict{"assert": Assert()}
	for k, v := range o.Predeclared {
		out[k] = v
	}
	return out
}

func (o Options) limits() script.Limits {
	if o.Limits == (script.Limits{}) {
		return script.DefaultLimits
	}
	return o.Limits
}

// Compiler returns the compiler for Test under opts. Test files are checked
// with assert and opts.Predeclared predeclared, for themselves and for the
// modules they load.
func Compiler(opts Options) bundle.AnyCompiler {
	pre := opts.predeclared()
	names := make([]string, 0, len(pre))
	for k := range pre {
		names = append(names, k)
	}
	sort.Strings(names)
	sc := starcompile.Options{
		Aliases:           opts.Aliases,
		Check:             check.Env{Predeclared: names},
		ModulePredeclared: pre,
		Native:            opts.Native,
		Limits:            opts.limits(),
	}
	return bundle.Compile(Test, starcompile.Compiler(sc, func(_ context.Context, u *starcompile.Unit) (bundle.Compiled[*starcompile.Unit], []bundle.Diagnostic) {
		if !IsTestFile(u.Path()) {
			return bundle.Compiled[*starcompile.Unit]{}, []bundle.Diagnostic{u.Errorf(CodePath, script.Position{}, "test file %s must end in _test.star", u.Path())}
		}
		return bundle.Compiled[*starcompile.Unit]{Value: u}, nil
	}))
}

// Outcome is the result of one test, or of a test file's top level when
// Test is empty. A file-level outcome appears when the top level fails, and
// when it passes but printed: its Output holds what the top level (and the
// modules it loaded) printed, so that output is never charged to a test.
type Outcome struct {
	File   string        `json:"file"`             // test file path in its bundle
	Test   string        `json:"test,omitempty"`   // test function; empty for the file's top level
	Err    *script.Error `json:"error,omitempty"`  // nil when the test (or top level) passed
	Steps  uint64        `json:"steps"`            // execution steps the test (or top level) used
	Output []string      `json:"output,omitempty"` // print() output, as "file:line: msg"
}

// Passed reports whether the outcome is a pass.
func (o Outcome) Passed() bool { return o.Err == nil }

// Name is "file" or "file::test".
func (o Outcome) Name() string {
	if o.Test == "" {
		return o.File
	}
	return o.File + "::" + o.Test
}

// IsTestFile reports whether p names a Starlark test file, which a Test
// contribution's path must.
func IsTestFile(p string) bool { return strings.HasSuffix(p, "_test.star") }

// Load loads the bundle rooted at fsys, which must contain bundle.yaml, with
// opts.Dependencies, and returns its Test definitions sorted by path. It
// fails when the bundle declares none.
func Load(ctx context.Context, fsys fs.FS, opts Options) ([]bundle.Definition[*starcompile.Unit], error) {
	const origin = "startest"
	cat, err := bundle.Load(ctx, bundle.Options{
		Sources:   append([]bundle.Source{bundle.Embedded(origin, fsys)}, opts.Dependencies...),
		Compilers: append([]bundle.AnyCompiler{Compiler(opts)}, opts.Compilers...),
	})
	if err != nil {
		return nil, err
	}
	var out []bundle.Definition[*starcompile.Unit]
	for _, def := range bundle.All(cat, Test) {
		if def.Owner.Origin == origin {
			out = append(out, def)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("startest: the bundle declares no %s contributions", Test.ID())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Value.Path() < out[j].Value.Path() })
	return out, nil
}

// Run loads the bundle rooted at fsys (see Load) and runs every test file it
// declares as a subtest of t, and each of its tests as a subtest of that. A
// bundle that fails to load fails t with every diagnostic.
func Run(t *testing.T, fsys fs.FS, opts Options) {
	t.Helper()
	defs, err := Load(t.Context(), fsys, opts)
	if err != nil {
		var le *bundle.LoadError
		if errors.As(err, &le) {
			for _, d := range le.Diagnostics {
				t.Errorf("%s:%d:%d: %s: %s", d.Path, d.Line, d.Column, d.Code, d.Message)
			}
		}
		t.Fatalf("startest: %v", err)
	}
	for _, def := range defs {
		t.Run(def.Value.Path(), func(t *testing.T) {
			for _, o := range RunUnit(t.Context(), def.Value, opts) {
				report := func(t *testing.T) {
					for _, line := range o.Output {
						t.Log(line)
					}
					if o.Err != nil {
						t.Error(o.Err.Error())
						for i := len(o.Err.Backtrace) - 1; i >= 0; i-- {
							fr := o.Err.Backtrace[i]
							t.Logf("  at %s (%s:%d:%d)", fr.Func, fr.Pos.File, fr.Pos.Line, fr.Pos.Col)
						}
					}
				}
				if o.Test == "" {
					report(t)
					continue
				}
				t.Run(o.Test, report)
			}
		})
	}
}

// RunUnit runs the tests in one test file, normally a Test definition's
// value. opts should be the Options its compiler was built with. A file
// whose top level fails yields one outcome with an empty Test. Otherwise
// there is one outcome per test_* function, preceded by a passing
// file-level outcome (empty Test) carrying the top level's print() output
// when it printed. A file defining no test_* functions yields one failing
// file-level outcome: a test file with no tests is a failure, not a pass, so
// renamed or mistyped test names cannot silently green a run.
//
// opts.Limits.Timeout bounds the top level including the modules it loads;
// running out is an Invalid error with TimedOut set, as in script.Exec.
func RunUnit(ctx context.Context, u *starcompile.Unit, opts Options) []Outcome {
	if ctx == nil {
		ctx = context.Background()
	}
	p := u.Path()
	lim := opts.limits()
	predeclared := opts.predeclared()
	loader := u.Loader(predeclared)

	var output []string
	print := func(th *starlark.Thread, msg string) {
		pos := th.CallFrame(1).Pos
		output = append(output, fmt.Sprintf("%s:%d: %s", pos.Filename(), pos.Line, msg))
	}
	flush := func() []string { o := output; output = nil; return o }

	data := u.Source()
	// As in script.Exec, the timeout is a context deadline so module loads
	// honour it too; it is told apart from the caller's own cancellation.
	runCtx, cancel := ctx, context.CancelFunc(func() {})
	if lim.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, lim.Timeout)
	}
	thread := script.NewThread("test:"+p, lim, print)
	loader.Bind(runCtx, thread, p)
	var globals starlark.StringDict
	_, err := script.Run(runCtx, script.Limits{}, thread, func() (starlark.Value, error) {
		prog, err := dialect.Compile(p, data, predeclared.Has)
		if err != nil {
			return nil, err
		}
		globals, err = prog.Init(thread, predeclared)
		globals.Freeze()
		return starlark.None, err
	})
	convertedCancel := false
	if err == nil && runCtx.Err() != nil {
		err = runCtx.Err()
		convertedCancel = true
	}
	// As in script.Exec: only the deadline's own cancellation (or a
	// success converted after it) is a timeout; a genuine error that
	// returned after the deadline keeps its own message.
	timedOut := lim.Timeout > 0 && ctx.Err() == nil &&
		errors.Is(runCtx.Err(), context.DeadlineExceeded) &&
		(convertedCancel || script.IsCancellation(err, "context done: "+context.DeadlineExceeded.Error()))
	cancel()
	steps := thread.ExecutionSteps()
	if err != nil {
		return []Outcome{{File: p, Err: script.Failure(ctx, p, err, timedOut, steps), Steps: steps, Output: flush()}}
	}
	out := make([]Outcome, 0, 1+len(globals))
	if top := flush(); len(top) > 0 {
		out = append(out, Outcome{File: p, Steps: steps, Output: top})
	}
	var names []string
	for name := range globals {
		if strings.HasPrefix(name, "test_") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		msg := fmt.Errorf("no test_* functions in %s", p)
		out = append(out, Outcome{File: p, Err: script.Failure(ctx, p, msg, false, steps), Steps: steps, Output: flush()})
		return out
	}
	for _, name := range names {
		o := Outcome{File: p, Test: name}
		var fn starlark.Callable
		switch v := globals[name].(type) {
		case *starlark.Function:
			if v.NumParams() > 0 {
				msg := fmt.Errorf("%s: test functions take no parameters", name)
				o.Err = script.Failure(ctx, p, msg, false, 0)
				o.Err.Pos = script.PositionOf(v.Position())
				out = append(out, o)
				continue
			}
			fn = v
		case starlark.Callable:
			fn = v // a host built-in bound to a test_* name still runs
		default:
			msg := fmt.Errorf("%s: test_* names must be functions, got %s", name, globals[name].Type())
			o.Err = script.Failure(ctx, p, msg, false, 0)
			out = append(out, o)
			continue
		}
		_, steps, err := script.Call(ctx, lim, p+"::"+name, fn, nil, print)
		o.Steps = steps
		if err != nil {
			o.Err = err.(*script.Error)
		}
		o.Output = flush()
		out = append(out, o)
	}
	return out
}

// Assert returns the assert module test files see.
func Assert() starlark.Value {
	return &starlarkstruct.Module{Name: "assert", Members: starlark.StringDict{
		"eq":       starlark.NewBuiltin("assert.eq", assertCompare(syntax.EQL, "want", "%s != %s")),
		"ne":       starlark.NewBuiltin("assert.ne", assertCompare(syntax.NEQ, "other", "%s == %s")),
		"true":     starlark.NewBuiltin("assert.true", assertTruth(true)),
		"false":    starlark.NewBuiltin("assert.false", assertTruth(false)),
		"contains": starlark.NewBuiltin("assert.contains", assertContains),
		"fails":    starlark.NewBuiltin("assert.fails", assertFails),
	}}
}

func failf(msg, format string, args ...any) error {
	s := fmt.Sprintf(format, args...)
	if msg != "" {
		s = msg + ": " + s
	}
	return fmt.Errorf("assertion failed: %s", s)
}

func assertCompare(op syntax.Token, second, format string) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var x, y starlark.Value
		var msg string
		if err := starlark.UnpackArgs(b.Name(), args, kwargs, "got", &x, second, &y, "msg?", &msg); err != nil {
			return nil, err
		}
		ok, err := starlark.Compare(op, x, y)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, failf(msg, format, x.String(), y.String())
		}
		return starlark.None, nil
	}
}

func assertTruth(want bool) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var x starlark.Value
		var msg string
		if err := starlark.UnpackArgs(b.Name(), args, kwargs, "cond", &x, "msg?", &msg); err != nil {
			return nil, err
		}
		if bool(x.Truth()) != want {
			return nil, failf(msg, "%s is not %v", x.String(), want)
		}
		return starlark.None, nil
	}
}

func assertContains(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var container, item starlark.Value
	var msg string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "container", &container, "item", &item, "msg?", &msg); err != nil {
		return nil, err
	}
	in, err := starlark.Binary(syntax.IN, item, container)
	if err != nil {
		return nil, err
	}
	if !bool(in.Truth()) {
		return nil, failf(msg, "%s does not contain %s", container.String(), item.String())
	}
	return starlark.None, nil
}

func assertFails(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var fn starlark.Callable
	var substr string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "fn", &fn, "substring", &substr); err != nil {
		return nil, err
	}
	_, err := starlark.Call(thread, fn, nil, nil)
	if err == nil {
		return nil, failf("", "%s did not fail (want an error containing %q)", fn.Name(), substr)
	}
	msg := err.Error()
	if ee, ok := err.(*starlark.EvalError); ok {
		msg = ee.Msg
	}
	if !strings.Contains(msg, substr) {
		return nil, failf("", "%s failed with %q, want an error containing %q", fn.Name(), msg, substr)
	}
	return starlark.String(msg), nil
}
