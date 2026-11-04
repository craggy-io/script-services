package script_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/craggy-io/script-services/dialect"
	"github.com/craggy-io/script-services/modules"
	"github.com/craggy-io/script-services/script"
	"github.com/craggy-io/script-services/values"
)

var lim = script.Limits{MaxSteps: 100_000, Timeout: 5 * time.Second}

// get is a plain host built-in standing in for a capability call.
var get = starlark.NewBuiltin("get", func(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	raw, err := values.KwargsToJSON(b.Name(), args, kwargs)
	if err != nil {
		return nil, err
	}
	var in struct{ ID string }
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	return values.FromGo(map[string]any{"got": in.ID})
})

func TestExecRunsBodyThenEntry(t *testing.T) {
	var mu sync.Mutex
	var prints []string
	src := `
seen = []
for k in ["a", "b"]:
    seen.append(get(id = k)["got"])
print("seen", seen)

def run(input):
    return {"seen": seen, "who": input["who"], "host": host_value()}
`
	host := starlark.StringDict{
		"get": get,
		"host_value": starlark.NewBuiltin("host_value", func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
			return starlark.String("h"), nil
		}),
	}
	res, err := script.Exec(context.Background(), script.Program{
		Filename: "nb.star", Source: []byte(src), Input: map[string]any{"who": "me"}, Predeclared: host,
	}, script.Options{Limits: lim, Print: func(pos script.Position, msg string) {
		mu.Lock()
		defer mu.Unlock()
		prints = append(prints, msg+"@"+pos.File+":"+strconv.Itoa(pos.Line))
	}})
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Value) != `{"host":"h","seen":["a","b"],"who":"me"}` {
		t.Fatalf("result = %s", res.Value)
	}
	if res.Steps == 0 {
		t.Fatal("no steps counted")
	}
	if len(prints) != 1 || prints[0] != `seen ["a", "b"]@nb.star:5` {
		t.Fatalf("prints = %q", prints)
	}
	if len(res.Modules) != 0 || res.Digest != modules.Digest([]byte(src), nil) {
		t.Fatalf("digest/modules = %s %v", res.Digest, res.Modules)
	}
}

func TestExecEntryForms(t *testing.T) {
	cases := []struct{ src, entry, want string }{
		{"x = 1\n", "", "null"},
		{"def run():\n    return 7\n", "", "7"},
		{"def main(i):\n    return i\n", "main", `{"k":[1,2.5]}`},
		{"run = 3\n", "", "null"}, // not a function: no entry
	}
	for _, c := range cases {
		res, err := script.Exec(context.Background(), script.Program{Source: []byte(c.src), Entry: c.entry, Input: map[string]any{"k": []any{1, 2.5}}}, script.Options{Limits: lim})
		if err != nil || string(res.Value) != c.want {
			t.Errorf("%q: %s, %v; want %s", c.src, res.Value, err, c.want)
		}
	}
}

type hostErr struct{ reason string }

func (e *hostErr) Error() string { return "host refused: " + e.reason }

func TestExecErrorsCarryPositionAndCode(t *testing.T) {
	host := starlark.StringDict{
		"get": get,
		"refuse": starlark.NewBuiltin("refuse", func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
			return nil, &hostErr{reason: "no_capacity"}
		}),
	}
	cases := []struct {
		name, src string
		line      int
		msg       string
	}{
		{"syntax", "x = (\n", 2, "got end of file"},
		{"undefined", "x = 1\ny = nope\n", 2, "undefined: nope"},
		{"runtime", "def f():\n    return 1 // 0\nf()\n", 2, "division by zero"},
		{"host", "x = 1\nrefuse()\n", 2, "host refused: no_capacity"},
		{"bad args", "get(1, 2)\n", 1, "pass arguments as keywords or one dict"},
		{"frozen input", "input['x'] = 1\n", 1, "frozen"},
		{"steps", "n = 0\nwhile True:\n    n += 1\n", 3, "too many steps"},
		{"recursion", "def f(n):\n    return f(n)\nf(1)\n", 2, "called recursively"},
		{"result", "def run():\n    return run\n", 0, "run() result is not JSON-convertible"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := script.Exec(context.Background(), script.Program{Filename: "e.star", Source: []byte(tc.src), Predeclared: host, Input: map[string]any{}},
				script.Options{Limits: script.Limits{MaxSteps: 10_000, Timeout: 5 * time.Second}})
			var se *script.Error
			if !errors.As(err, &se) {
				t.Fatalf("err = %v (%T)", err, err)
			}
			if se.Code != script.Invalid || se.TimedOut {
				t.Fatalf("code = %s timedOut = %v (%v)", se.Code, se.TimedOut, err)
			}
			if se.Pos.Line != tc.line || (tc.line > 0 && se.Pos.File != "e.star") {
				t.Fatalf("pos = %+v, want line %d (%v)", se.Pos, tc.line, err)
			}
			if !strings.Contains(se.Message, tc.msg) {
				t.Fatalf("message %q lacks %q", se.Message, tc.msg)
			}
		})
	}
}

func TestErrorUnwrapFindsHostErrors(t *testing.T) {
	host := starlark.StringDict{"refuse": starlark.NewBuiltin("refuse", func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
		return nil, &hostErr{reason: "no_capacity"}
	})}
	src := "def helper():\n    refuse()\n\ndef run():\n    helper()\n"
	_, err := script.Exec(context.Background(), script.Program{Filename: "r.star", Source: []byte(src), Predeclared: host}, script.Options{Limits: lim})
	var he *hostErr
	if !errors.As(err, &he) || he.reason != "no_capacity" {
		t.Fatalf("errors.As did not find the host error in %v", err)
	}
	var se *script.Error
	errors.As(err, &se)
	var funcs []string
	for _, f := range se.Backtrace {
		funcs = append(funcs, f.Func+":"+strconv.Itoa(f.Pos.Line))
	}
	if got := strings.Join(funcs, " "); got != "run:5 helper:2 refuse:0" {
		t.Fatalf("backtrace = %s", got)
	}
	b, _ := json.Marshal(se)
	if !strings.Contains(string(b), `"code":"invalid"`) || !strings.Contains(string(b), `"pos":{"file":"r.star","line":2,"col":11}`) {
		t.Fatalf("Error JSON = %s", b)
	}
	if se.Error() != "invalid: r.star:2:11: host refused: no_capacity" {
		t.Fatalf("Error() = %s", se.Error())
	}
}

func TestExecInputClashIsInvalid(t *testing.T) {
	_, err := script.Exec(context.Background(), script.Program{Source: []byte("x = 1"), Predeclared: starlark.StringDict{"input": starlark.None}}, script.Options{})
	var se *script.Error
	if !errors.As(err, &se) || se.Code != script.Invalid || !strings.Contains(se.Message, `"input"`) {
		t.Fatalf("err = %v", err)
	}
	_, err = script.Exec(context.Background(), script.Program{Source: []byte("x = 1"), Input: make(chan int)}, script.Options{})
	if !errors.As(err, &se) || !strings.Contains(se.Message, "not JSON-encodable") {
		t.Fatalf("bad input err = %v", err)
	}
}

func TestExecCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	_, err := script.Exec(ctx, script.Program{Source: []byte("while True:\n    pass\n")}, script.Options{Limits: script.Limits{Timeout: 5 * time.Second}})
	var se *script.Error
	if !errors.As(err, &se) || se.Code != script.Cancelled || se.TimedOut || se.Steps == 0 {
		t.Fatalf("err = %v (%+v)", err, se)
	}
}

func TestExecTimeout(t *testing.T) {
	_, err := script.Exec(context.Background(), script.Program{Source: []byte("while True:\n    pass\n")}, script.Options{Limits: script.Limits{Timeout: 50 * time.Millisecond}})
	var se *script.Error
	if !errors.As(err, &se) || se.Code != script.Invalid || !se.TimedOut || se.Message != "script exceeded its time limit" {
		t.Fatalf("err = %v (%+v)", err, se)
	}
	// The time limit also covers modules the script loads.
	l := modules.NewLoader(modules.Map(map[string]string{"spin.star": "while True:\n    pass\n"}), modules.Options{})
	_, err = script.Exec(context.Background(), script.Program{Source: []byte("load('spin.star', 'x')\n"), Modules: l}, script.Options{Limits: script.Limits{Timeout: 50 * time.Millisecond}})
	if !errors.As(err, &se) || se.Code != script.Invalid || !se.TimedOut {
		t.Fatalf("module timeout err = %v (%+v)", err, se)
	}
}

func TestExecLoad(t *testing.T) {
	_, err := script.Exec(context.Background(), script.Program{Filename: "m.star", Source: []byte("load('lib.star', 'x')\n")}, script.Options{})
	var se *script.Error
	if !errors.As(err, &se) || !strings.Contains(se.Message, "no module source") || se.Pos.Line != 1 {
		t.Fatalf("no-source err = %v", err)
	}
	files := map[string]string{
		"lib/text.star": "load('./util.star', 'twice')\ndef shout(s):\n    return twice(s.upper())\n",
		"lib/util.star": "def twice(s):\n    return s + s\n",
	}
	src := "load('./lib/text.star', 'shout')\ndef run(input):\n    return shout(input)\n"
	exec := func(files map[string]string) script.Result {
		t.Helper()
		l := modules.NewLoader(modules.Map(files), modules.Options{})
		res, err := script.Exec(context.Background(), script.Program{Filename: "main.star", Source: []byte(src), Input: "ab", Modules: l}, script.Options{Limits: lim})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := exec(files)
	if string(res.Value) != `"ABAB"` || len(res.Modules) != 2 || res.Modules[0].Path != "lib/text.star" || res.Modules[1].Path != "lib/util.star" {
		t.Fatalf("result = %s, modules = %v", res.Value, res.Modules)
	}
	if res.Digest != modules.Digest([]byte(src), []modules.Dep{{Spec: "./lib/text.star", Digest: res.Modules[0].Digest}}) {
		t.Fatal("Result.Digest != modules.Digest(source, direct deps)")
	}
	if again := exec(files); again.Digest != res.Digest {
		t.Fatal("digest not deterministic")
	}
	changed := map[string]string{"lib/text.star": files["lib/text.star"], "lib/util.star": "def twice(s):\n    return s + s + ''\n"}
	if other := exec(changed); other.Digest == res.Digest {
		t.Fatal("digest did not change with a transitive dependency")
	}
	// A failing module surfaces with both positions.
	l := modules.NewLoader(modules.Map(map[string]string{"bad.star": "x = 1\ny = 1 // 0\n"}), modules.Options{})
	_, err = script.Exec(context.Background(), script.Program{Filename: "main.star", Source: []byte("\nload('bad.star', 'x')\n"), Modules: l}, script.Options{})
	if !errors.As(err, &se) || se.Pos.Line != 2 || !strings.Contains(se.Message, "cannot load bad.star: bad.star:2:7: floored division by zero") {
		t.Fatalf("module failure err = %v", err)
	}
	var me *modules.Error
	if !errors.As(err, &me) || me.Path != "bad.star" {
		t.Fatalf("modules.Error not reachable from %v", err)
	}
}

func TestCallAndRun(t *testing.T) {
	prog := starlark.StringDict{}
	th := script.NewThread("defs", script.Limits{}, nil)
	g, err := starlark.ExecFileOptions(dialect.FileOptions, th, "defs.star", "def add(a, b):\n    return a + b\ndef bad():\n    return {}[1]\ndef spin():\n    while True:\n        pass\n", prog)
	if err != nil {
		t.Fatal(err)
	}
	v, steps, err := script.Call(context.Background(), lim, "add", g["add"].(starlark.Callable), starlark.Tuple{starlark.MakeInt(2), starlark.MakeInt(3)}, nil)
	if err != nil || v != starlark.MakeInt(5) || steps == 0 {
		t.Fatalf("add = %v, %d, %v", v, steps, err)
	}
	_, _, err = script.Call(context.Background(), lim, "bad", g["bad"].(starlark.Callable), nil, nil)
	var se *script.Error
	if !errors.As(err, &se) || se.Pos.File != "defs.star" || se.Pos.Line != 4 || !strings.Contains(se.Message, "key 1 not in dict") {
		t.Fatalf("bad err = %v", err)
	}
	_, _, err = script.Call(context.Background(), script.Limits{Timeout: 30 * time.Millisecond}, "spin", g["spin"].(starlark.Callable), nil, nil)
	if !errors.As(err, &se) || !se.TimedOut || se.Code != script.Invalid {
		t.Fatalf("spin err = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = script.Call(ctx, lim, "add", g["add"].(starlark.Callable), starlark.Tuple{starlark.MakeInt(2), starlark.MakeInt(3)}, nil)
	if !errors.As(err, &se) || se.Code != script.Cancelled {
		t.Fatalf("cancelled call err = %v", err)
	}
	// Run alone: a thread cancelled by its own timeout.
	th2 := script.NewThread("run", script.Limits{MaxSteps: 50}, nil)
	_, err = script.Run(context.Background(), script.Limits{}, th2, func() (starlark.Value, error) {
		return starlark.Call(th2, g["spin"], nil, nil)
	})
	if err == nil || !strings.Contains(err.Error(), "too many steps") {
		t.Fatalf("Run step limit err = %v", err)
	}
}

func TestFailureClassifies(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := script.Failure(ctx, "x.star", errors.New("boom"), true, 3)
	if e.Code != script.Cancelled || e.Steps != 3 || e.Message != "boom" {
		t.Fatalf("cancelled failure = %+v", e)
	}
	e = script.Failure(context.Background(), "x.star", errors.New("boom"), true, 0)
	if e.Code != script.Invalid || !e.TimedOut || e.Message != script.TimeLimitMessage {
		t.Fatalf("timeout failure = %+v", e)
	}
	if script.PositionOf(syntax.Position{}) != (script.Position{}) {
		t.Fatal("zero position")
	}
}

// A file gets one digest whether it runs as a program or loads as a module,
// and wherever it lives: both cover the source plus (spec, digest) pairs of
// direct dependencies only.
func TestProgramAndModuleDigestsAgree(t *testing.T) {
	mainSrc := "load('./lib/a.star', 'A')\ndef run():\n    return A\n"
	files := func(dir string) map[string]string {
		return map[string]string{
			dir + "main.star":  mainSrc,
			dir + "lib/a.star": "load('./b.star', 'B')\nA = B + 1\n",
			dir + "lib/b.star": "load('./c.star', 'C')\nB = C + 1\n",
			dir + "lib/c.star": "C = 1\n",
		}
	}
	program := func(dir string) script.Result {
		t.Helper()
		l := modules.NewLoader(modules.Map(files(dir)), modules.Options{})
		res, err := script.Exec(context.Background(), script.Program{Filename: dir + "main.star", Source: []byte(mainSrc), Modules: l}, script.Options{Limits: lim})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := program("")
	if string(res.Value) != "3" || len(res.Modules) != 3 {
		t.Fatalf("result = %s, modules = %v", res.Value, res.Modules)
	}
	_, ref, err := modules.NewLoader(modules.Map(files("")), modules.Options{}).Module(context.Background(), "main.star")
	if err != nil {
		t.Fatal(err)
	}
	if res.Digest != ref.Digest {
		t.Fatalf("program digest %s != module digest %s", res.Digest, ref.Digest)
	}
	if moved := program("x/y/"); moved.Digest != res.Digest {
		t.Fatalf("program digest depends on its path: %s != %s", moved.Digest, res.Digest)
	}
}

// A single resolve error's message is just the message: Error() adds the
// position once. With several, each later one keeps its own position.
func TestResolveErrorPositionNotDuplicated(t *testing.T) {
	exec := func(src string) *script.Error {
		t.Helper()
		_, err := script.Exec(context.Background(), script.Program{Filename: "bad.star", Source: []byte(src)}, script.Options{})
		var se *script.Error
		if !errors.As(err, &se) {
			t.Fatalf("err = %v", err)
		}
		return se
	}
	se := exec("x = 1\ny = nope\n")
	if se.Message != "undefined: nope" || se.Error() != "invalid: bad.star:2:5: undefined: nope" {
		t.Fatalf("one error: message %q, Error() %q", se.Message, se.Error())
	}
	se = exec("x = 1\ny = nope\nz = nada\n")
	if se.Message != "undefined: nope; 3:5: undefined: nada" || se.Error() != "invalid: bad.star:2:5: undefined: nope; 3:5: undefined: nada" {
		t.Fatalf("two errors: message %q, Error() %q", se.Message, se.Error())
	}
	// The same rule holds for a module's resolve errors.
	l := modules.NewLoader(modules.Map(map[string]string{"m.star": "a = nope\nb = nada\n", "one.star": "a = nope\n"}), modules.Options{})
	if _, _, err := l.Module(context.Background(), "one.star"); err == nil || err.Error() != "one.star:1:5: undefined: nope" {
		t.Fatalf("module one error = %v", err)
	}
	if _, _, err := l.Module(context.Background(), "m.star"); err == nil || err.Error() != "m.star:1:5: undefined: nope; 2:5: undefined: nada" {
		t.Fatalf("module two errors = %v", err)
	}
}
