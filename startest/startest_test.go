package startest_test

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"go.starlark.net/starlark"

	"github.com/craggy-io/script-services/modules"
	"github.com/craggy-io/script-services/script"
	"github.com/craggy-io/script-services/startest"
)

var passing = fstest.MapFS{
	"lib/text.star": {Data: []byte("def shout(s):\n    return s.upper() + '!'\n")},
	"lib/text_test.star": {Data: []byte(`
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
`)},
	"top_test.star": {Data: []byte("load('lib/text.star', 'shout')\ndef test_top():\n    assert.eq(shout('a'), 'A!', msg = 'shouting')\n")},
}

var hostBuiltin = starlark.StringDict{"host": starlark.NewBuiltin("host", func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return starlark.String("h"), nil
})}

func TestRunPassing(t *testing.T) {
	startest.Run(t, passing, startest.Options{Predeclared: hostBuiltin})
}

func TestRunFileOutcomes(t *testing.T) {
	src := modules.Map(map[string]string{
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
	outs := startest.RunFile(context.Background(), src, "x_test.star", startest.Options{Limits: script.Limits{MaxSteps: 10_000}})
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

func TestRunFileTopLevelFailure(t *testing.T) {
	src := modules.Map(map[string]string{
		"bad_test.star":     "x = 1\nload('nope.star', 'y')\n",
		"syntax_test.star":  "def test_x(:\n",
		"limited_test.star": "def test_a():\n    pass\n",
	})
	outs := startest.RunFile(context.Background(), src, "bad_test.star", startest.Options{})
	if len(outs) != 1 || outs[0].Test != "" || outs[0].Passed() || outs[0].Err.Pos.Line != 2 || !strings.Contains(outs[0].Err.Message, "cannot load nope.star") {
		t.Fatalf("outs = %+v", outs)
	}
	outs = startest.RunFile(context.Background(), src, "syntax_test.star", startest.Options{})
	if len(outs) != 1 || outs[0].Passed() || outs[0].Err.Pos.Line != 1 {
		t.Fatalf("syntax outs = %+v", outs)
	}
	outs = startest.RunFile(context.Background(), src, "missing_test.star", startest.Options{})
	if len(outs) != 1 || outs[0].Passed() || !strings.Contains(outs[0].Err.Message, "does not exist") {
		t.Fatalf("missing outs = %+v", outs)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	outs = startest.RunFile(ctx, src, "limited_test.star", startest.Options{})
	if len(outs) != 1 || outs[0].Passed() || outs[0].Err.Code != script.Cancelled {
		t.Fatalf("cancelled outs = %+v", outs)
	}
}

func TestSeparateModuleSource(t *testing.T) {
	tests := modules.Map(map[string]string{"t_test.star": "load('lib.star', 'V')\ndef test_v():\n    assert.eq(V, 'from modules')\n"})
	mods := modules.Map(map[string]string{"lib.star": "V = 'from modules'\n"})
	outs := startest.RunFile(context.Background(), tests, "t_test.star", startest.Options{Modules: mods})
	if len(outs) != 1 || !outs[0].Passed() {
		t.Fatalf("outs = %+v", outs)
	}
}

func TestFiles(t *testing.T) {
	files, err := startest.Files(passing)
	if err != nil || strings.Join(files, ",") != "lib/text_test.star,top_test.star" {
		t.Fatalf("Files = %v, %v", files, err)
	}
}

// Limits.Timeout also bounds module loading at the file's top level.
func TestTimeoutCoversModuleLoading(t *testing.T) {
	src := modules.Map(map[string]string{
		"spin.star":      "def spin():\n    while True:\n        pass\nspin()\n",
		"slow_test.star": "load('spin.star', 'spin')\ndef test_x():\n    pass\n",
	})
	// The outer deadline only stops a regression from hanging the suite.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	outs := startest.RunFile(ctx, src, "slow_test.star", startest.Options{Limits: script.Limits{Timeout: 100 * time.Millisecond}})
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("RunFile took %v with a 100ms timeout", elapsed)
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
	src := modules.Map(map[string]string{
		"p_test.star": "print('setup')\ndef test_a():\n    print('in a')\ndef test_b():\n    pass\n",
		"q_test.star": "def test_a():\n    pass\n",
	})
	outs := startest.RunFile(context.Background(), src, "p_test.star", startest.Options{})
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
	if outs := startest.RunFile(context.Background(), src, "q_test.star", startest.Options{}); len(outs) != 1 || outs[0].Test != "test_a" {
		t.Fatalf("silent file outs = %+v", outs)
	}
}
