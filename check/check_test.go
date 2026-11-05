package check_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"go.starlark.net/starlark"

	"github.com/craggy-io/script-services/check"
	"github.com/craggy-io/script-services/modules"
)

func closedObject(required []string, props ...string) *jsonschema.Schema {
	s := &jsonschema.Schema{Type: "object", Properties: map[string]*jsonschema.Schema{}, Required: required,
		AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}}}
	for _, p := range props {
		s.Properties[p] = &jsonschema.Schema{Type: "string"}
	}
	return s
}

// env mirrors the registry fixture: storage.artifact.get at versions 1 and
// 2, slow.work, a call() ref function and jobs.start, named "capability".
func env() check.Env {
	get := check.Callable{Name: "storage.artifact.get", Input: closedObject([]string{"id"}, "id"), Value: "get@2"}
	slow := check.Callable{Name: "slow.work", Input: closedObject(nil, "id"), Value: "slow@1"}
	ping := check.Callable{Name: "net.ping"} // takes no input
	byRef := map[string]check.Callable{
		"storage.artifact.get": get, "storage.artifact.get@2": get,
		"storage.artifact.get@1": {Name: "storage.artifact.get@1", Input: get.Input, Value: "get@1"},
		"slow.work":              slow, "slow.work@1": slow,
	}
	resolve := func(ref string) (check.Callable, bool) {
		c, ok := byRef[ref]
		return c, ok
	}
	return check.Env{
		Callables:   []check.Callable{get, slow, ping, {Name: "jobs.list", Input: closedObject(nil)}},
		Predeclared: []string{"helper"},
		RefFuncs:    map[string]func(string) (check.Callable, bool){"call": resolve, "jobs.start": resolve},
		Noun:        "capability",
	}
}

func lines(diags []check.Diagnostic) string {
	var got []string
	for _, d := range diags {
		got = append(got, strconv.Itoa(d.Pos.Line)+" "+d.Message)
	}
	return strings.Join(got, "\n")
}

func TestCheckReportsUnknownCallablesAndArguments(t *testing.T) {
	src := `
a = storage.artifact.gte(id = "x")
b = storage.artifact.get(idd = "x")
c = storage.artifact(id = "x")
d = call("storage.artifact.got", id = "x")
e = jobs.start("slow.work", id = "y")
f = storage.artifact.get({"id": "ok"})
g = helper(missing_name)
h = net.ping(n = 1)
i = jobs.list()
j = call("storage.artifact.gte@3")
k = storage.artifact.get.more
l = jobs.start.x
m = jobs.start("slow.work", idd = 1)
def run(input):
    return [x.upper() for x in input["names"]]
`
	want := []string{
		"2 unknown capability storage.artifact.gte (did you mean storage.artifact.get?)",
		`3 storage.artifact.get has no input "idd" (did you mean id?)`,
		`3 storage.artifact.get requires input "id"`,
		"4 storage.artifact is a namespace, not a capability",
		`5 unknown capability "storage.artifact.got" (did you mean storage.artifact.get?)`,
		"8 undefined: missing_name",
		"9 net.ping takes no input",
		`11 unknown capability "storage.artifact.gte@3" (did you mean storage.artifact.get?)`,
		`12 storage.artifact.get is a capability, not a namespace: it has no attribute "more"`,
		`13 jobs.start is a function, not a namespace: it has no attribute "x"`,
		`14 slow.work has no input "idd" (did you mean id?)`,
	}
	diags := check.Check(env(), "c.star", []byte(src))
	if got := lines(diags); got != strings.Join(want, "\n") {
		t.Fatalf("diagnostics:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
	for _, d := range diags {
		if d.Severity != "error" || d.Pos.File != "c.star" || d.Pos.Col == 0 {
			t.Fatalf("diagnostic %+v", d)
		}
	}
	if d := check.Check(env(), "ok.star", []byte("x = storage.artifact.get(id = 'a')\ny = call('slow.work@1')\n")); len(d) != 0 {
		t.Fatalf("clean script: %+v", d)
	}
}

func TestClosedSchemaListsInputsWithoutSuggestion(t *testing.T) {
	e := check.Env{Callables: []check.Callable{{Name: "a.b", Input: closedObject(nil, "alpha", "beta")}}}
	got := lines(check.Check(e, "", []byte("a.b(zzzzzz = 1)\n")))
	if got != `1 a.b has no input "zzzzzz" (inputs: alpha, beta)` {
		t.Fatalf("got %s", got)
	}
	// An open schema accepts unknown names.
	open := check.Env{Callables: []check.Callable{{Name: "a.b", Input: &jsonschema.Schema{Type: "object"}}}}
	if d := check.Check(open, "", []byte("a.b(zzzzzz = 1)\n")); len(d) != 0 {
		t.Fatalf("open schema: %+v", d)
	}
}

func TestAnalyzeListsUsesOncePerName(t *testing.T) {
	src := `
a = storage.artifact.get(id = "x")
b = jobs.start("slow.work", id = "y")
c = storage.artifact.get(id = "z")
d = call("slow.work", id = "w")
e = storage.artifact.gte(id = "x")
f = call("storage.artifact.get@1", id = "q")
`
	got := check.Analyze(env(), "u.star", []byte(src))
	var uses []string
	for _, u := range got.Uses {
		uses = append(uses, u.Name+"="+u.Value.(string))
	}
	if want := "storage.artifact.get=get@2 slow.work=slow@1 storage.artifact.get@1=get@1"; strings.Join(uses, " ") != want {
		t.Fatalf("uses = %v, want %s", uses, want)
	}
	if len(got.Diagnostics) != 1 || !strings.Contains(got.Diagnostics[0].Message, "storage.artifact.gte") {
		t.Fatalf("diagnostics = %+v, want the one unknown callable", got.Diagnostics)
	}
}

func TestRefFuncRootWithoutCallables(t *testing.T) {
	e := check.Env{RefFuncs: map[string]func(string) (check.Callable, bool){
		"jobs.start": func(ref string) (check.Callable, bool) { return check.Callable{}, false },
	}}
	got := lines(check.Check(e, "", []byte("j = jobs.start('x.y')\nk = jobs.stop()\n")))
	want := "1 unknown callable \"x.y\"\n2 unknown callable jobs.stop"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	// input is always predeclared; nothing else is.
	if got := lines(check.Check(check.Env{}, "", []byte("x = input\ny = call\n"))); got != "2 undefined: call" {
		t.Fatalf("bare env: %s", got)
	}
}

func TestCheckSyntaxError(t *testing.T) {
	d := check.Check(check.Env{}, "s.star", []byte("x = (\n"))
	if len(d) != 1 || d[0].Pos.Line != 2 || d[0].Pos.File != "s.star" || !strings.Contains(d[0].Message, "got end of file") {
		t.Fatalf("diags = %+v", d)
	}
}

func TestCheckLoads(t *testing.T) {
	src := modules.Map(map[string]string{
		"lib/text.star":   "load('./util.star', 'twice')\ndef shout(s):\n    return twice(s)\nCONST = 1\nfor i in range(2):\n    LOOPED = i\n",
		"lib/broken.star": "def f(:\n",
	})
	script := `load("./lib/text.star", "shout", "CONST", "LOOPED", "shuot")
load("lib/broken.star", "f")
load("lib/missing.star", "g")
load("../outside.star", "h")
load("//lib/text.star", "twice")
load("lib/text.star", "_private")
`
	a := check.Analyze(check.Env{Modules: src}, "main.star", []byte(script))
	want := []string{
		`1 load "./lib/text.star": lib/text.star does not define shuot (did you mean shout?)`,
		`2 load "lib/broken.star": lib/broken.star does not parse: lib/broken.star:1:8: got ':', want ')'`,
		`3 load "lib/missing.star": cannot read lib/missing.star: read lib/missing.star: file does not exist`,
		`4 load "../outside.star": module path "../outside.star" escapes the module root`,
		`5 load "//lib/text.star": lib/text.star does not define twice`,
		`6 load: names with leading underscores are not exported: _private`,
	}
	if got := lines(a.Diagnostics); got != strings.Join(want, "\n") {
		t.Fatalf("diagnostics:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
	if got := strings.Join(a.Loads, " "); got != "lib/text.star lib/broken.star lib/missing.star" {
		t.Fatalf("loads = %s", got)
	}
	// Without a module source, loads are listed but not looked up.
	b := check.Analyze(check.Env{}, "main.star", []byte("load('lib/x.star', 'y')\nz = y\n"))
	if len(b.Diagnostics) != 0 || len(b.Loads) != 1 || b.Loads[0] != "lib/x.star" {
		t.Fatalf("no-source analysis = %+v", b)
	}
}

// Messages name callables with Env.Noun: "callable" by default, so the
// package speaks no host's vocabulary; a capability registry passes
// "capability".
func TestNounNamesCallables(t *testing.T) {
	src := "a = storage.artifact.gte(id = 'x')\nb = storage.artifact(id = 'x')\nc = call('nope')\nd = storage.artifact.get.more\n"
	want := func(noun string) string {
		return strings.Join([]string{
			"1 unknown " + noun + " storage.artifact.gte (did you mean storage.artifact.get?)",
			"2 storage.artifact is a namespace, not a " + noun,
			`3 unknown ` + noun + ` "nope"`,
			`4 storage.artifact.get is a ` + noun + `, not a namespace: it has no attribute "more"`,
		}, "\n")
	}
	e := env()
	e.Noun = ""
	if got := lines(check.Check(e, "n.star", []byte(src))); got != want("callable") {
		t.Fatalf("default noun:\n%s\nwant:\n%s", got, want("callable"))
	}
	e.Noun = "capability"
	if got := lines(check.Check(e, "n.star", []byte(src))); got != want("capability") {
		t.Fatalf("capability noun:\n%s\nwant:\n%s", got, want("capability"))
	}
}

// A native module's load is checked against its members, not read from
// Modules.
func TestNativeLoads(t *testing.T) {
	env := check.Env{Native: map[string]modules.Native{"@host/clock": {Members: starlark.StringDict{"now": starlark.None}}}}
	a := check.Analyze(env, "s.star", []byte("load('@host/clock', 'now', 'nwo')\n"))
	if len(a.Diagnostics) != 1 || !strings.Contains(a.Diagnostics[0].Message, "does not define nwo (did you mean now?)") {
		t.Fatalf("diagnostics = %+v", a.Diagnostics)
	}
	if len(a.Loads) != 1 || a.Loads[0] != "@host/clock" {
		t.Fatalf("loads = %v", a.Loads)
	}
}
