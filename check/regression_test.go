package check_test

import (
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/craggy-io/script-services/check"
	"github.com/craggy-io/script-services/modules"
)

func diagsContain(t *testing.T, a check.Analysis, substr string) bool {
	t.Helper()
	for _, d := range a.Diagnostics {
		if strings.Contains(d.Message, substr) {
			return true
		}
	}
	return false
}

// Module-internal resolve failures used to be discarded: check said clean
// for modules the Loader then refused to compile.
func TestCheckModuleResolveErrors(t *testing.T) {
	src := modules.Map(map[string]string{
		"lib/usesinput.star": "x = input\n",
		"lib/dup.star":       "def f(a, a):\n    return a\n",
		"lib/useshost.star":  "x = host\n",
		"lib/ok.star":        "def twice(s):\n    return s + s\n",
	})
	a := check.Analyze(check.Env{Modules: src}, "main.star",
		[]byte("load('./lib/usesinput.star', 'x')\nload('./lib/dup.star', 'f')\nload('./lib/useshost.star', 'x')\n"))
	if !diagsContain(t, a, "undefined: input") {
		t.Fatalf("input-in-module not diagnosed: %+v", a.Diagnostics)
	}
	if !diagsContain(t, a, "duplicate parameter") {
		t.Fatalf("duplicate parameter not diagnosed: %+v", a.Diagnostics)
	}
	if !diagsContain(t, a, "undefined: host") {
		t.Fatalf("host-in-module not diagnosed by default: %+v", a.Diagnostics)
	}

	// The same modules resolve against explicitly declared module names.
	b := check.Analyze(check.Env{Modules: src, ModulePredeclared: []string{"host"}}, "main.star",
		[]byte("load('./lib/useshost.star', 'x')\nload('./lib/ok.star', 'twice')\n"))
	if len(b.Diagnostics) != 0 {
		t.Fatalf("ModulePredeclared diags = %+v", b.Diagnostics)
	}
}

// A kwarg matching a patternProperties pattern is known even in a closed
// input schema.
func TestCheckPatternPropertiesExempt(t *testing.T) {
	in := &jsonschema.Schema{
		Type:                 "object",
		Properties:           map[string]*jsonschema.Schema{},
		PatternProperties:    map[string]*jsonschema.Schema{"^x_": {}},
		AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}},
	}
	env := check.Env{Callables: []check.Callable{{Name: "ns.get", Input: in}}}
	if d := check.Check(env, "main.star", []byte("ns.get(x_foo = 1)\n")); len(d) != 0 {
		t.Fatalf("pattern kwarg diags = %+v", d)
	}
	if d := check.Check(env, "main.star", []byte("ns.get(other = 1)\n")); len(d) != 1 {
		t.Fatalf("unknown kwarg diags = %+v", d)
	}
}
