package startest_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"go.starlark.net/starlark"

	"github.com/craggy-io/foundation-kit/bundle"

	"github.com/craggy-io/script-services/script"
	"github.com/craggy-io/script-services/starcompile"
	"github.com/craggy-io/script-services/startest"
)

// bundleFS is a bundle in memory: its hand-written bundle.yaml plus files.
func bundleFS(manifest string, files map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{"bundle.yaml": {Data: []byte(manifest)}}
	for name, src := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(src)}
	}
	return fsys
}

// unit loads the bundle and returns its test contribution id.
func unit(t *testing.T, fsys fstest.MapFS, id string, opts startest.Options) *starcompile.Unit {
	t.Helper()
	defs, err := startest.Load(context.Background(), fsys, opts)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, d := range defs {
		if d.Ref.ID == id {
			return d.Value
		}
	}
	t.Fatalf("no test contribution %q", id)
	return nil
}

var passing = bundleFS(`apiVersion: craggy.io/bundle/v1
metadata: {id: test.passing, version: 1.0.0}
contributions:
  - {id: text-test, kind: craggy.script-test@1, path: lib/text_test.star}
  - {id: top-test, kind: craggy.script-test@1, path: top_test.star}
`, map[string]string{
	"lib/text.star": "def shout(s):\n    return s.upper() + '!'\n",
	"lib/text_test.star": `
load("./text.star", "shout")

def test_shout():
    assert.eq(shout("hi"), "HI!")
    assert.ne(shout("hi"), "hi")

def test_asserts():
    assert.true([1])
    assert.false({})
    assert.contains([1, 2, 3], 2)
    assert.contains("haystack", "st")
    assert.contains({"k": 1}, "k")
    msg = assert.fails(lambda: {}["missing"], "not in dict")
    assert.contains(msg, "missing")
    n = 0
    while n < 3:          # the dialect: while and set() are allowed
        n += 1
    assert.eq(set([1, 1]), set([1]))
    assert.eq(host(), "h")

def helper():
    return "not a test"
`,
	"top_test.star": "load('lib/text.star', 'shout')\ndef test_top():\n    assert.eq(shout('a'), 'A!', msg = 'shouting')\n",
})

var hostBuiltin = starlark.StringDict{"host": starlark.NewBuiltin("host", func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return starlark.String("h"), nil
})}

func TestRunPassing(t *testing.T) {
	startest.Run(t, passing, startest.Options{Predeclared: hostBuiltin})
}

const xManifest = `apiVersion: craggy.io/bundle/v1
metadata: {id: test.x, version: 1.0.0}
contributions:
  - {id: x, kind: craggy.script-test@1, path: x_test.star}
`

func TestRunUnitOutcomes(t *testing.T) {
	fsys := bundleFS(xManifest, map[string]string{
		"lib/m.star": "V = 1\n",
		"x_test.star": `load("lib/m.star", "V")

def test_ok():
    print("value", V)
    assert.eq(V, 1)

def test_eq():
    assert.eq([1, 2], [1, 3], msg = "lists")

def test_ne():
    assert.ne(1, 1)

def test_true():
    assert.true(0)

def test_false():
    assert.false("x")

def test_contains():
    assert.contains([1], 2)

def test_fails_passes():
    assert.fails(lambda: 1, "boom")

def test_fails_wrong_message():
    assert.fails(lambda: 1 // 0, "boom")

def test_runtime():
    return {}["k"]

def test_spin():
    while True:
        pass

def test_params(x):
    pass
`,
	})
	opts := startest.Options{Limits: script.Limits{MaxSteps: 10_000}}
	outs := startest.RunUnit(context.Background(), unit(t, fsys, "x", opts), opts)
	got := map[string]startest.Outcome{}
	var names []string
	for _, o := range outs {
		got[o.Test] = o
		names = append(names, o.Test)
	}
	if strings.Join(names, ",") != "test_contains,test_eq,test_fails_passes,test_fails_wrong_message,test_false,test_ne,test_ok,test_params,test_runtime,test_spin,test_true" {
		t.Fatalf("tests = %v", names)
	}
	if o := got["test_ok"]; !o.Passed() || len(o.Output) != 1 || o.Output[0] != "x_test.star:4: value 1" || o.Steps == 0 {
		t.Fatalf("test_ok = %+v", o)
	}
	want := map[string]struct {
		line int
		msg  string
	}{
		"test_eq":                  {8, "assertion failed: lists: [1, 2] != [1, 3]"},
		"test_ne":                  {11, "assertion failed: 1 == 1"},
		"test_true":                {14, "assertion failed: 0 is not true"},
		"test_false":               {17, `assertion failed: "x" is not false`},
		"test_contains":            {20, "assertion failed: [1] does not contain 2"},
		"test_fails_passes":        {23, `assertion failed: lambda did not fail (want an error containing "boom")`},
		"test_fails_wrong_message": {26, `assertion failed: lambda failed with "floored division by zero", want an error containing "boom"`},
		"test_runtime":             {29, `key "k" not in dict`},
		"test_spin":                {31, "too many steps"},
		"test_params":              {35, "test functions take no parameters"},
	}
	for name, w := range want {
		o := got[name]
		if o.Passed() {
			t.Errorf("%s passed", name)
			continue
		}
		if o.Err.Pos.Line != w.line || o.Err.Pos.File != "x_test.star" || !strings.Contains(o.Err.Message, w.msg) {
			t.Errorf("%s: %s:%d %q; want line %d containing %q", name, o.Err.Pos.File, o.Err.Pos.Line, o.Err.Message, w.line, w.msg)
		}
	}
	if o := got["test_eq"]; o.Name() != "x_test.star::test_eq" {
		t.Errorf("Name = %s", o.Name())
	}
}

// Problems a static check can see fail the load, with positions; problems
// only running can find fail the file's top level.
func TestTopLevelFailures(t *testing.T) {
	fsys := bundleFS(`apiVersion: craggy.io/bundle/v1
metadata: {id: test.bad, version: 1.0.0}
contributions:
  - {id: bad, kind: craggy.script-test@1, path: bad_test.star}
  - {id: syntax, kind: craggy.script-test@1, path: syntax_test.star}
  - {id: misnamed, kind: craggy.script-test@1, path: misnamed.star}
`, map[string]string{
		"bad_test.star":    "x = 1\nload('nope.star', 'y')\n",
		"syntax_test.star": "def test_x(:\n",
		"misnamed.star":    "def test_x():\n    pass\n",
	})
	_, err := startest.Load(context.Background(), fsys, startest.Options{})
	var le *bundle.LoadError
	if !errors.As(err, &le) {
		t.Fatalf("Load err = %v", err)
	}
	got := map[string]bundle.Diagnostic{}
	for _, d := range le.Diagnostics {
		got[d.Contribution] = d
	}
	if d := got["bad"]; d.Code != starcompile.CodeCheck || d.Path != "bad_test.star" || d.Line != 2 || !strings.Contains(d.Message, "cannot read nope.star") {
		t.Errorf("bad = %+v", d)
	}
	if d := got["syntax"]; d.Code != starcompile.CodeCheck || d.Line != 1 {
		t.Errorf("syntax = %+v", d)
	}
	if d := got["misnamed"]; d.Code != startest.CodePath {
		t.Errorf("misnamed = %+v", d)
	}

	fsys = bundleFS(`apiVersion: craggy.io/bundle/v1
metadata: {id: test.runtime, version: 1.0.0}
contributions:
  - {id: div, kind: craggy.script-test@1, path: div_test.star}
`, map[string]string{"div_test.star": "x = 1\ny = x // 0\ndef test_a():\n    pass\n"})
	u := unit(t, fsys, "div", startest.Options{})
	outs := startest.RunUnit(context.Background(), u, startest.Options{})
	if len(outs) != 1 || outs[0].Test != "" || outs[0].Passed() || outs[0].Err.Pos.Line != 2 || !strings.Contains(outs[0].Err.Message, "division by zero") {
		t.Fatalf("outs = %+v", outs)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	outs = startest.RunUnit(ctx, u, startest.Options{})
	if len(outs) != 1 || outs[0].Passed() || outs[0].Err.Code != script.Cancelled {
		t.Fatalf("cancelled outs = %+v", outs)
	}
}

// A test file loads modules of its bundle, and of a declared dependency as
// "@<dependency-id>/path".
func TestLoadsFromDependency(t *testing.T) {
	lib := bundleFS(`apiVersion: craggy.io/bundle/v1
metadata: {id: test.lib, version: 1.0.0}
contributions: []
`, map[string]string{"lib.star": "V = 'from the library'\n"})
	fsys := bundleFS(`apiVersion: craggy.io/bundle/v1
metadata: {id: test.app, version: 1.0.0}
dependencies:
  - id: test.lib
contributions:
  - {id: t, kind: craggy.script-test@1, path: t_test.star}
`, map[string]string{"t_test.star": "load('@test.lib/lib.star', 'V')\ndef test_v():\n    assert.eq(V, 'from the library')\n"})
	opts := startest.Options{Dependencies: []bundle.Source{bundle.Embedded("lib", lib)}}
	outs := startest.RunUnit(context.Background(), unit(t, fsys, "t", opts), opts)
	if len(outs) != 1 || !outs[0].Passed() {
		t.Fatalf("outs = %+v", outs)
	}
}

// Only declared contributions are tests: an undeclared *_test.star file is
// not discovered, and a folder without bundle.yaml is not a bundle.
func TestLoadFindsDeclaredTestsOnly(t *testing.T) {
	defs, err := startest.Load(context.Background(), passing, startest.Options{Predeclared: hostBuiltin})
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, d := range defs {
		paths = append(paths, d.Value.Path())
	}
	if strings.Join(paths, ",") != "lib/text_test.star,top_test.star" {
		t.Fatalf("paths = %v", paths)
	}
	undeclared := bundleFS(`apiVersion: craggy.io/bundle/v1
metadata: {id: test.none, version: 1.0.0}
contributions: []
`, map[string]string{"x_test.star": "def test_x():\n    pass\n"})
	if _, err := startest.Load(context.Background(), undeclared, startest.Options{}); err == nil || !strings.Contains(err.Error(), "declares no craggy.script-test@1") {
		t.Fatalf("undeclared Load err = %v", err)
	}
	loose := fstest.MapFS{"x_test.star": {Data: []byte("def test_x():\n    pass\n")}}
	if _, err := startest.Load(context.Background(), loose, startest.Options{}); err == nil || !strings.Contains(err.Error(), string(bundle.CodeManifestMissing)) {
		t.Fatalf("no manifest Load err = %v", err)
	}
}

// Limits.Timeout also bounds module loading at the file's top level.
func TestTimeoutCoversModuleLoading(t *testing.T) {
	fsys := bundleFS(`apiVersion: craggy.io/bundle/v1
metadata: {id: test.slow, version: 1.0.0}
contributions:
  - {id: slow, kind: craggy.script-test@1, path: slow_test.star}
`, map[string]string{
		"spin.star":      "def spin():\n    while True:\n        pass\nspin()\n",
		"slow_test.star": "load('spin.star', 'spin')\ndef test_x():\n    pass\n",
	})
	// The outer deadline only stops a regression from hanging the suite.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	opts := startest.Options{Limits: script.Limits{Timeout: 100 * time.Millisecond}}
	u := unit(t, fsys, "slow", opts)
	start := time.Now()
	outs := startest.RunUnit(ctx, u, opts)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("RunUnit took %v with a 100ms timeout", elapsed)
	}
	if len(outs) != 1 || outs[0].Test != "" || outs[0].Passed() {
		t.Fatalf("outs = %+v", outs)
	}
	if e := outs[0].Err; e.Code != script.Invalid || !e.TimedOut || e.Message != script.TimeLimitMessage {
		t.Fatalf("err = %+v", e)
	}
}

// print() at a file's top level belongs to the file, not to its first test.
func TestTopLevelPrintGoesToFileOutcome(t *testing.T) {
	fsys := bundleFS(`apiVersion: craggy.io/bundle/v1
metadata: {id: test.print, version: 1.0.0}
contributions:
  - {id: p, kind: craggy.script-test@1, path: p_test.star}
  - {id: q, kind: craggy.script-test@1, path: q_test.star}
`, map[string]string{
		"p_test.star": "print('setup')\ndef test_a():\n    print('in a')\ndef test_b():\n    pass\n",
		"q_test.star": "def test_a():\n    pass\n",
	})
	outs := startest.RunUnit(context.Background(), unit(t, fsys, "p", startest.Options{}), startest.Options{})
	if len(outs) != 3 {
		t.Fatalf("outs = %+v", outs)
	}
	if f := outs[0]; f.Test != "" || !f.Passed() || len(f.Output) != 1 || f.Output[0] != "p_test.star:1: setup" || f.Steps == 0 {
		t.Fatalf("file outcome = %+v", f)
	}
	if a := outs[1]; a.Test != "test_a" || len(a.Output) != 1 || a.Output[0] != "p_test.star:3: in a" {
		t.Fatalf("test_a = %+v", a)
	}
	if b := outs[2]; b.Test != "test_b" || len(b.Output) != 0 {
		t.Fatalf("test_b = %+v", b)
	}
	// A silent, passing top level yields no file outcome.
	if outs := startest.RunUnit(context.Background(), unit(t, fsys, "q", startest.Options{}), startest.Options{}); len(outs) != 1 || outs[0].Test != "test_a" {
		t.Fatalf("silent file outs = %+v", outs)
	}
}
