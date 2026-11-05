package starcompile_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"go.starlark.net/starlark"

	"github.com/craggy-io/foundation-kit/bundle"
	"github.com/craggy-io/foundation-kit/kind"

	"github.com/craggy-io/script-services/check"
	"github.com/craggy-io/script-services/modules"
	"github.com/craggy-io/script-services/script"
	"github.com/craggy-io/script-services/starcompile"
)

// bundleFS is a bundle in memory: its hand-written bundle.yaml plus files.
func bundleFS(manifest string, files map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{"bundle.yaml": {Data: []byte(manifest)}}
	for name, src := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(src)}
	}
	return fsys
}

// lib is a library bundle other bundles depend on.
var lib = bundleFS(`apiVersion: craggy.io/bundle/v1
metadata: {id: test.lib, version: 1.0.0}
contributions: []
`, map[string]string{
	"text/up.star":   "load('./bang.star', 'bang')\nload('//text/same.star', 'same')\ndef up(s):\n    return bang(same(s.upper()))\n",
	"text/bang.star": "def bang(s):\n    return s + '!'\n",
	"text/same.star": "def same(s):\n    return s\n",
	"text/up2.star":  "load('../../x.star', 'y')\nx = 1\n",
	"boom.star":      "def boom():\n    return 1 // 0\nX = boom()\n",
})

func load(t *testing.T, app fstest.MapFS, compilers ...bundle.AnyCompiler) (*bundle.Catalog, error) {
	t.Helper()
	return bundle.Load(context.Background(), bundle.Options{
		Sources:   []bundle.Source{bundle.Embedded("app", app), bundle.Embedded("lib", lib)},
		Compilers: compilers,
	})
}

func diagnostics(t *testing.T, err error) []bundle.Diagnostic {
	t.Helper()
	var le *bundle.LoadError
	if !errors.As(err, &le) {
		t.Fatalf("err = %v, want a *bundle.LoadError", err)
	}
	return le.Diagnostics
}

func script1(path, src string) fstest.MapFS {
	return bundleFS(`apiVersion: craggy.io/bundle/v1
metadata: {id: test.app, version: 1.0.0}
dependencies:
  - id: test.lib
contributions:
  - {id: s, kind: craggy.script@1, path: `+path+`}
`, map[string]string{path: src, "local/two.star": "TWO = 2\n", "local/boom.star": "def f():\n    return {}['k']\nY = f()\n"})
}

func only(t *testing.T, cat *bundle.Catalog) *starcompile.Unit {
	t.Helper()
	def, ok := bundle.Get(cat, starcompile.Script, bundle.Ref{Owner: "test.app", ID: "s"})
	if !ok {
		t.Fatal("no script s")
	}
	return def.Value
}

// Aliases map @alias to a declared dependency; an alias with no entry is
// the dependency's ID. Inside a dependency, // and ./ stay in that bundle.
func TestAliasesAndDependencyTrees(t *testing.T) {
	src := "load('@text/up.star', 'up')\nload('@test.lib/text/bang.star', 'bang')\nload('local/two.star', 'TWO')\ndef run():\n    return bang(up('hi')) + str(TWO)\n"
	cat, err := load(t, script1("main.star", src), starcompile.Scripts(starcompile.Options{Aliases: map[string]string{"text": "test.lib"}}))
	if err == nil {
		t.Fatal("@text/up.star resolved to text/up.star in test.lib, want @text/ to be the root of test.lib")
	}
	src = "load('@lib/text/up.star', 'up')\nload('@test.lib/text/bang.star', 'bang')\nload('local/two.star', 'TWO')\ndef run():\n    return bang(up('hi')) + str(TWO)\n"
	cat, err = load(t, script1("main.star", src), starcompile.Scripts(starcompile.Options{Aliases: map[string]string{"lib": "test.lib"}}))
	if err != nil {
		t.Fatal(err)
	}
	u := only(t, cat)
	res, err := script.Exec(context.Background(), script.Program{Filename: u.Path(), Source: u.Source(), Modules: u.Loader(nil)}, script.Options{Limits: u.Limits()})
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Value) != `"HI!!2"` {
		t.Fatalf("value = %s", res.Value)
	}
	// The static digest is the digest running reports.
	if res.Digest != u.Ref().Digest || u.Ref().Path != "main.star" {
		t.Fatalf("digest %s, Ref %+v", res.Digest, u.Ref())
	}
	var paths []string
	for _, m := range res.Modules {
		paths = append(paths, m.Path)
	}
	if strings.Join(paths, ",") != "@lib/text/bang.star,@lib/text/same.star,@lib/text/up.star,@test.lib/text/bang.star,local/two.star" {
		t.Fatalf("modules = %v", paths)
	}
	if len(u.Deps()) != 3 {
		t.Fatalf("deps = %+v", u.Deps())
	}
}

func TestUndeclaredAliasNamesTheAlias(t *testing.T) {
	_, err := load(t, script1("main.star", "load('@other/x.star', 'x')\n"), starcompile.Scripts(starcompile.Options{}))
	ds := diagnostics(t, err)
	if len(ds) != 1 || ds[0].Code != starcompile.CodeCheck || ds[0].Line != 1 || !strings.Contains(ds[0].Message, "load alias @other") {
		t.Fatalf("diagnostics = %+v", ds)
	}
	_, err = load(t, script1("main.star", "load('@test.lib/text/up2.star', 'x')\n"), starcompile.Scripts(starcompile.Options{}))
	ds = diagnostics(t, err)
	if len(ds) != 1 || ds[0].Code != starcompile.CodeLoad || !strings.Contains(ds[0].Message, "escapes the module root") {
		t.Fatalf("escape diagnostics = %+v", ds)
	}
}

func TestNotAStarFile(t *testing.T) {
	_, err := load(t, script1("main.txt", "x = 1\n"), starcompile.Scripts(starcompile.Options{}))
	if ds := diagnostics(t, err); len(ds) != 1 || ds[0].Code != starcompile.CodeSource {
		t.Fatalf("diagnostics = %+v", ds)
	}
}

// Decl is a domain value built by running the top level with a fresh
// collector per contribution.
type Decl struct {
	Names []string
	Sum   int
}

var declKind = kind.New[Decl]("test.decl", 1)

func declCompiler() bundle.AnyCompiler {
	opts := starcompile.Options{
		Aliases: map[string]string{"lib": "test.lib"},
		Check:   check.Env{Predeclared: []string{"declare"}},
	}
	return bundle.Compile(declKind, starcompile.Compiler(opts, func(ctx context.Context, u *starcompile.Unit) (bundle.Compiled[Decl], []bundle.Diagnostic) {
		var d Decl
		declare := starlark.NewBuiltin("declare", func(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			var name string
			if err := starlark.UnpackArgs(b.Name(), args, kwargs, "name", &name); err != nil {
				return nil, err
			}
			d.Names = append(d.Names, name)
			return starlark.None, nil
		})
		ex, err := u.Exec(ctx, starcompile.ExecOptions{Predeclared: starlark.StringDict{"declare": declare}})
		if err != nil {
			return bundle.Compiled[Decl]{}, u.Diagnostics(err)
		}
		n, ok := ex.Globals["SUM"].(starlark.Int)
		if !ok {
			return bundle.Compiled[Decl]{}, []bundle.Diagnostic{u.Errorf("test.decl.sum", script.Position{}, "SUM must be an int")}
		}
		d.Sum, _ = starlark.AsInt32(n)
		return bundle.Compiled[Decl]{Value: d}, nil
	}))
}

func declBundle(files map[string]string) fstest.MapFS {
	return bundleFS(`apiVersion: craggy.io/bundle/v1
metadata: {id: test.app, version: 1.0.0}
dependencies:
  - id: test.lib
contributions:
  - {id: d, kind: test.decl@1, path: d.star}
`, files)
}

func TestDomainCompilerRunsTopLevel(t *testing.T) {
	cat, err := load(t, declBundle(map[string]string{"d.star": "load('two.star', 'TWO')\ndeclare('a')\ndeclare(name = 'b')\nSUM = TWO + 1\n", "two.star": "TWO = 2\n"}), declCompiler())
	if err != nil {
		t.Fatal(err)
	}
	def, _ := bundle.Get(cat, declKind, bundle.Ref{Owner: "test.app", ID: "d"})
	if strings.Join(def.Value.Names, ",") != "a,b" || def.Value.Sum != 3 {
		t.Fatalf("value = %+v", def.Value)
	}

	// Check errors stop before build; a domain problem is the domain's code;
	// a runtime failure is positioned in the failing file.
	cases := []struct {
		files map[string]string
		code  bundle.Code
		path  string
		line  int
		msg   string
	}{
		{map[string]string{"d.star": "declare(nope)\n"}, starcompile.CodeCheck, "d.star", 1, "undefined: nope"},
		{map[string]string{"d.star": "declare('a')\n"}, "test.decl.sum", "d.star", 0, "SUM must be an int"},
		{map[string]string{"d.star": "x = 1\nSUM = x // 0\n"}, starcompile.CodeExec, "d.star", 2, "division by zero"},
		{map[string]string{"d.star": "load('m.star', 'Y')\nSUM = 1\n", "m.star": "def f():\n    return {}['k']\nY = f()\n"}, starcompile.CodeExec, "m.star", 2, `key "k" not in dict`},
		{map[string]string{"d.star": "load('@lib/boom.star', 'X')\nSUM = 1\n"}, starcompile.CodeExec, "d.star", 0, "@lib/boom.star:2:"},
		{map[string]string{"d.star": "load('m.star', 'Y')\nSUM = 1\n", "m.star": "Y = 1\nload('n.star', 'Z')\n", "n.star": "Z = (\n"}, starcompile.CodeLoad, "n.star", 2, "want primary expression"},
	}
	for i, c := range cases {
		_, err := load(t, declBundle(c.files), declCompiler())
		ds := diagnostics(t, err)
		if len(ds) != 1 {
			t.Errorf("%d: diagnostics = %+v", i, ds)
			continue
		}
		d := ds[0]
		if d.Code != c.code || d.Path != c.path || d.Line != c.line || !strings.Contains(d.Message, c.msg) || d.Bundle != "test.app" || d.Contribution != "d" {
			t.Errorf("%d: %+v; want %s at %s:%d containing %q", i, d, c.code, c.path, c.line, c.msg)
		}
	}
}

func TestExecMutableAndNative(t *testing.T) {
	clock := modules.Native{Members: starlark.StringDict{"NOW": starlark.MakeInt(42)}, Version: "1"}
	opts := starcompile.Options{Native: map[string]modules.Native{"@host/clock": clock}}
	app := script1("main.star", "load('@host/clock', 'NOW')\nL = [NOW]\n")
	cat, err := load(t, app, starcompile.Scripts(opts))
	if err != nil {
		t.Fatal(err)
	}
	u := only(t, cat)
	ex, err := u.Exec(context.Background(), starcompile.ExecOptions{Mutable: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := ex.Globals["L"].(*starlark.List).Append(starlark.None); err != nil {
		t.Fatalf("Mutable globals are frozen: %v", err)
	}
	if len(ex.Modules) != 1 || ex.Modules[0].Path != "@host/clock" || ex.Modules[0].Digest != modules.NativeDigest("@host/clock", "1") {
		t.Fatalf("modules = %+v", ex.Modules)
	}
	frozen, err := u.Exec(context.Background(), starcompile.ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := frozen.Globals["L"].(*starlark.List).Append(starlark.None); err == nil {
		t.Fatal("default globals are not frozen")
	}

	// A native member the module lacks is a check error; a new version is
	// a new digest.
	if _, err := load(t, script1("main.star", "load('@host/clock', 'LATER')\n"), starcompile.Scripts(opts)); err == nil || !strings.Contains(err.Error(), "does not define LATER") {
		t.Fatalf("missing member err = %v", err)
	}
	clock.Version = "2"
	cat2, err := load(t, app, starcompile.Scripts(starcompile.Options{Native: map[string]modules.Native{"@host/clock": clock}}))
	if err != nil {
		t.Fatal(err)
	}
	if only(t, cat2).Ref().Digest == u.Ref().Digest {
		t.Fatal("a new native version kept the digest")
	}
}

// New is the compiler's check outside Load, and its Unit is the same.
func TestNewMatchesCompiler(t *testing.T) {
	cat, err := load(t, script1("main.star", "load('local/two.star', 'TWO')\n"), starcompile.Scripts(starcompile.Options{}))
	if err != nil {
		t.Fatal(err)
	}
	u := only(t, cat)
	again, diags := starcompile.New(context.Background(), u.Contribution(), starcompile.Options{})
	if again == nil || len(diags) != 0 || again.Ref() != u.Ref() || len(again.Analysis().Loads) != 1 {
		t.Fatalf("New = %+v, %+v", again, diags)
	}
}
