package starcompile

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"go.starlark.net/starlark"

	"github.com/craggy-io/foundation-kit/bundle"
	"github.com/craggy-io/foundation-kit/kind"

	"github.com/craggy-io/script-services/check"
	"github.com/craggy-io/script-services/dialect"
	"github.com/craggy-io/script-services/modules"
	"github.com/craggy-io/script-services/script"
)

// Diagnostic codes this package reports. Domains add their own, namespaced by
// their kind.
const (
	// CodeSource: the contribution's path is not a readable .star file.
	CodeSource bundle.Code = "script.source"
	// CodeCheck: the static check (package check) found a problem.
	CodeCheck bundle.Code = "script.check"
	// CodeLoad: a module the script loads, directly or transitively, is
	// missing, does not parse, or is part of a load cycle.
	CodeLoad bundle.Code = "script.load"
	// CodeExec: running the top level failed (see Unit.Diagnostics).
	CodeExec bundle.Code = "script.exec"
)

// Script is the generic script kind, craggy.script@1: a .star contribution
// that checks clean with only `input` predeclared. Its value is the checked
// Unit. The craggy-script CLI reads it; domains declare their own kinds and
// compile them with Compiler.
var Script = kind.New[*Unit]("craggy.script", 1)

// Scripts returns the compiler for Script under opts.
func Scripts(opts Options) bundle.AnyCompiler {
	return bundle.Compile(Script, Compiler(opts, func(_ context.Context, u *Unit) (bundle.Compiled[*Unit], []bundle.Diagnostic) {
		return bundle.Compiled[*Unit]{Value: u}, nil
	}))
}

// Options say how a kind's .star contributions are checked and run. A
// domain builds one per kind and passes it to Compiler. Options are retained
// by every Unit, so do not modify anything they reference afterwards.
type Options struct {
	// Aliases maps the alias in load("@alias/x.star") to the ID of a
	// dependency declared in bundle.yaml. An alias with no entry is the
	// dependency's bundle ID itself. See modules.FromContribution.
	Aliases map[string]string

	// Check describes what the host will predeclare for the script
	// (callables, other names, ref functions, the noun used in messages).
	// The compiler sets Check.Modules to the contribution's module source
	// and Check.Native to Native. When Check.ModulePredeclared is nil, it
	// is set to the names in ModulePredeclared.
	Check check.Env

	// ModulePredeclared are the built-ins load()ed modules see, besides
	// the Starlark universe. They are shared by every load, so they must
	// hold no per-script state.
	ModulePredeclared starlark.StringDict

	// Native are Go modules load() can name by exact spec, as in
	// modules.Options.Native.
	Native map[string]modules.Native

	// Limits bound Exec: MaxSteps the top level and each module's top
	// level, Timeout the whole Exec. Zero means script.DefaultLimits.
	Limits script.Limits
}

// Unit is one .star contribution that checked clean: its source, the module
// source its load() statements read, and its content address. It is
// immutable and safe for concurrent use. A Unit stays usable after
// bundle.Load returns, so a domain may keep it in its value and Exec or
// Loader it at run time.
type Unit struct {
	c        bundle.Contribution
	src      []byte
	mods     modules.Source
	ref      modules.Ref
	deps     []modules.Dep
	analysis check.Analysis
	opts     Options
}

// Compiler returns a bundle.Compiler for a kind whose contributions are .star
// files. For each contribution it reads the file, checks it statically
// against opts.Check with load() served by modules.FromContribution, and
// computes its digest without running it (modules.Address). Check errors and
// unloadable modules are diagnostics with file:line positions, and build is
// not called. Otherwise build turns the Unit into the domain's value: it may
// run the top level with Unit.Exec and its own predeclared built-ins, keep
// the Unit to run later, or both. Check warnings are added to build's
// diagnostics.
//
// Neither the compiler nor Unit has side effects beyond running Starlark
// with the built-ins the domain supplies, so the result depends only on the
// contribution, as bundle.Live's compile reuse requires. Domain built-ins
// passed to Exec at compile time must keep that true.
func Compiler[T any](opts Options, build func(ctx context.Context, u *Unit) (bundle.Compiled[T], []bundle.Diagnostic)) bundle.Compiler[T] {
	return func(ctx context.Context, c bundle.Contribution) (bundle.Compiled[T], []bundle.Diagnostic) {
		u, diags := New(ctx, c, opts)
		if u == nil {
			return bundle.Compiled[T]{}, diags
		}
		out, more := build(ctx, u)
		return out, append(diags, more...)
	}
}

// New checks one contribution as Compiler does, for hosts and tests that
// need a Unit outside bundle.Load. It returns a nil Unit when any
// diagnostic is an error.
func New(ctx context.Context, c bundle.Contribution, opts Options) (*Unit, []bundle.Diagnostic) {
	if ctx == nil {
		ctx = context.Background()
	}
	diag := func(code bundle.Code, sev bundle.Severity, pos script.Position, msg string) bundle.Diagnostic {
		return newDiagnostic(c, code, sev, pos, msg)
	}
	if !strings.HasSuffix(c.Path, ".star") {
		return nil, []bundle.Diagnostic{diag(CodeSource, bundle.SeverityError, script.Position{}, fmt.Sprintf("script %s must be a .star file", c.Path))}
	}
	src, err := fs.ReadFile(c.Files, c.Path)
	if err != nil {
		return nil, []bundle.Diagnostic{diag(CodeSource, bundle.SeverityError, script.Position{}, err.Error())}
	}
	u := &Unit{c: c, src: src, mods: modules.FromContribution(c, opts.Aliases), opts: opts}

	env := opts.Check
	env.Modules, env.Native = u.mods, opts.Native
	if env.ModulePredeclared == nil {
		env.ModulePredeclared = sortedKeys(opts.ModulePredeclared)
	}
	u.analysis = check.Analyze(env, c.Path, src)
	var diags []bundle.Diagnostic
	failed := false
	for _, d := range u.analysis.Diagnostics {
		sev := bundle.SeverityWarning
		if d.Severity == "error" {
			sev, failed = bundle.SeverityError, true
		}
		diags = append(diags, diag(CodeCheck, sev, d.Pos, d.Message))
	}
	if failed {
		return nil, diags
	}
	u.ref, u.deps, err = modules.Address(ctx, u.mods, modules.Options{Native: opts.Native}, c.Path, src)
	if err != nil {
		d := u.diagnostic(CodeLoad, err)
		return nil, append(diags, d)
	}
	return u, diags
}

func sortedKeys(d starlark.StringDict) []string {
	out := make([]string, 0, len(d))
	for k := range d {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Contribution returns the contribution the Unit was compiled from.
func (u *Unit) Contribution() bundle.Contribution { return u.c }

// Path returns the script's path in its bundle. It names the file in
// positions and is what "./" load() specs resolve against.
func (u *Unit) Path() string { return u.c.Path }

// Source returns a copy of the script's source.
func (u *Unit) Source() []byte { return append([]byte(nil), u.src...) }

// Modules returns the Source serving the script's load() statements:
// modules.FromContribution over the contribution with Options.Aliases.
func (u *Unit) Modules() modules.Source { return u.mods }

// Ref returns the script's path and digest. The digest is modules.Digest over
// the source and its direct dependencies, the same value script.Exec reports
// as Result.Digest when it runs the script.
func (u *Unit) Ref() modules.Ref { return u.ref }

// Deps returns the script's direct dependencies, sorted by spec.
func (u *Unit) Deps() []modules.Dep { return append([]modules.Dep(nil), u.deps...) }

// Analysis returns the static check's result: warnings, the callables the
// script uses and the modules it loads.
func (u *Unit) Analysis() check.Analysis { return u.analysis }

// Limits returns Options.Limits, or script.DefaultLimits when they are zero.
func (u *Unit) Limits() script.Limits {
	if u.opts.Limits == (script.Limits{}) {
		return script.DefaultLimits
	}
	return u.opts.Limits
}

// Loader returns a new modules.Loader for the script's load() statements,
// with Options.Native and the step bound from Limits. Modules see
// predeclared, or Options.ModulePredeclared when predeclared is nil. Use it
// to run the script with script.Exec, or to Bind it to a thread of your own;
// each call returns a fresh Loader with an empty cache.
func (u *Unit) Loader(predeclared starlark.StringDict) *modules.Loader {
	if predeclared == nil {
		predeclared = u.opts.ModulePredeclared
	}
	return modules.NewLoader(u.mods, modules.Options{Predeclared: predeclared, MaxSteps: u.Limits().MaxSteps, Native: u.opts.Native})
}

// ExecOptions configure one Unit.Exec.
type ExecOptions struct {
	// Predeclared are the script's host built-ins, for example a fresh
	// collector's declaration functions.
	Predeclared starlark.StringDict
	// ModulePredeclared, when not nil, replaces Options.ModulePredeclared
	// for the modules this run loads.
	ModulePredeclared starlark.StringDict
	// Thread, when not nil, runs the top level, so the caller can set
	// thread locals, a step bound or Print. Exec replaces its Load. When
	// nil, Exec uses a new thread bounded by Limits().MaxSteps.
	Thread *starlark.Thread
	// Print receives print() output when Thread is nil. nil discards it.
	Print func(pos script.Position, msg string)
	// Mutable leaves the globals unfrozen, for hosts whose script values
	// must stay live across calls. By default they are frozen, so they are
	// safe to share between goroutines.
	Mutable bool
}

// Execution is a top level that ran.
type Execution struct {
	Globals starlark.StringDict // the script's globals, frozen unless ExecOptions.Mutable
	Modules []modules.Ref       // every module loaded, transitively, sorted by path
	Steps   uint64              // execution steps the top level used
}

// Exec runs the script's top level with opts.Predeclared and a fresh Loader,
// under Limits(): Timeout bounds the run and the modules it loads, as in
// script.Exec. A failure is a *script.Error; Diagnostics converts it for a
// compiler's result.
func (u *Unit) Exec(ctx context.Context, opts ExecOptions) (Execution, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	lim := u.Limits()
	thread := opts.Thread
	if thread == nil {
		var print func(*starlark.Thread, string)
		if opts.Print != nil {
			print = func(th *starlark.Thread, msg string) { opts.Print(script.PositionOf(th.CallFrame(1).Pos), msg) }
		}
		thread = script.NewThread("script:"+u.c.Path, script.Limits{MaxSteps: lim.MaxSteps}, print)
	}
	runCtx, cancel := ctx, context.CancelFunc(func() {})
	if lim.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, lim.Timeout)
	}
	defer cancel()
	loader := u.Loader(opts.ModulePredeclared)
	loader.Bind(runCtx, thread, u.c.Path)
	predeclared := opts.Predeclared
	var globals starlark.StringDict
	_, err := script.Run(runCtx, script.Limits{}, thread, func() (starlark.Value, error) {
		prog, err := dialect.Compile(u.c.Path, u.src, predeclared.Has)
		if err != nil {
			return nil, err
		}
		globals, err = prog.Init(thread, predeclared)
		if !opts.Mutable {
			globals.Freeze()
		}
		return starlark.None, err
	})
	converted := false
	if err == nil && runCtx.Err() != nil {
		err, converted = runCtx.Err(), true
	}
	steps := thread.ExecutionSteps()
	if err != nil {
		timedOut := lim.Timeout > 0 && ctx.Err() == nil &&
			errors.Is(runCtx.Err(), context.DeadlineExceeded) &&
			(converted || script.IsCancellation(err, "context done: "+context.DeadlineExceeded.Error()))
		return Execution{Steps: steps}, script.Failure(ctx, u.c.Path, err, timedOut, steps)
	}
	return Execution{Globals: globals, Modules: loader.Refs(thread), Steps: steps}, nil
}

// Diagnostics converts an error from Exec, script.Exec or script.Call on this
// script into bundle diagnostics with code CodeExec, positioned at the
// innermost frame in the script, or in the failing module when a load
// failed. A nil err gives none.
func (u *Unit) Diagnostics(err error) []bundle.Diagnostic {
	if err == nil {
		return nil
	}
	return []bundle.Diagnostic{u.diagnostic(CodeExec, err)}
}

// Errorf returns an error diagnostic about this script at pos (zero for the
// whole file), for a domain's own problems with a script that ran, such as
// a missing declaration. code should be namespaced by the domain's kind.
func (u *Unit) Errorf(code bundle.Code, pos script.Position, format string, args ...any) bundle.Diagnostic {
	return newDiagnostic(u.c, code, bundle.SeverityError, pos, fmt.Sprintf(format, args...))
}

func (u *Unit) diagnostic(code bundle.Code, err error) bundle.Diagnostic {
	var (
		me *modules.Error
		se *script.Error
	)
	switch {
	case errors.As(err, &se) && !errors.As(se.Unwrap(), &me):
		return newDiagnostic(u.c, code, bundle.SeverityError, se.Pos, se.Message)
	case errors.As(err, &me):
		pos := script.PositionOf(me.Pos)
		pos.File = me.Path
		return newDiagnostic(u.c, code, bundle.SeverityError, pos, me.Msg)
	}
	return newDiagnostic(u.c, code, bundle.SeverityError, script.Position{}, err.Error())
}

// newDiagnostic places a diagnostic in c's bundle. A position in a module of
// a dependency ("@alias/...") is not a file of this bundle, so the
// diagnostic is placed on the contribution's own file and the message keeps
// the module's position.
func newDiagnostic(c bundle.Contribution, code bundle.Code, sev bundle.Severity, pos script.Position, msg string) bundle.Diagnostic {
	d := bundle.Diagnostic{Code: code, Severity: sev, Bundle: c.Bundle.ID, Contribution: c.Ref.ID, Path: c.Path, Message: msg}
	switch {
	case pos.File == "" || pos.File == c.Path:
		d.Line, d.Column = pos.Line, pos.Col
	case strings.HasPrefix(pos.File, "@"):
		where := pos.File
		if pos.Line > 0 {
			where = fmt.Sprintf("%s:%d:%d", pos.File, pos.Line, pos.Col)
		}
		d.Message = where + ": " + msg
	default:
		d.Path, d.Line, d.Column = pos.File, pos.Line, pos.Col
	}
	return d
}
