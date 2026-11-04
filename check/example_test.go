package check_test

import (
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/craggy-io/script-services/check"
	"github.com/craggy-io/script-services/modules"
)

// A host describes its callables and ref functions in an Env; Analyze
// reports diagnostics, the callables used and the load() targets, without
// running anything. This host is a capability registry, so it sets Noun.
func ExampleAnalyze() {
	getIn := &jsonschema.Schema{
		Type:                 "object",
		Properties:           map[string]*jsonschema.Schema{"id": {Type: "string"}, "depth": {Type: "integer"}},
		Required:             []string{"id"},
		AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}},
	}
	get := check.Callable{Name: "storage.artifact.get", Input: getIn, Value: "host payload"}
	env := check.Env{
		Callables:   []check.Callable{get},
		Predeclared: []string{"log"},
		RefFuncs: map[string]func(string) (check.Callable, bool){
			"call": func(ref string) (check.Callable, bool) {
				if ref == "storage.artifact.get" || ref == "storage.artifact.get@2" {
					return get, true
				}
				return check.Callable{}, false
			},
		},
		Modules: modules.Map(map[string]string{"lib/util.star": "def twice(s):\n    return s + s\n"}),
		// Messages say "callable" unless the host names its own kind.
		Noun: "capability",
	}
	src := `load("./lib/util.star", "twice", "thrice")
a = storage.artifact.get(id = "x")
b = storage.artifact.gte(id = "x")
c = storage.artifact.get(idd = "x")
d = call("storage.artifact.get@2", id = "y")
e = call("storage.artefact.get")
log(twice(input["name"]))
`
	a := check.Analyze(env, "main.star", []byte(src))
	for _, d := range a.Diagnostics {
		fmt.Printf("%d:%d %s: %s\n", d.Pos.Line, d.Pos.Col, d.Severity, d.Message)
	}
	for _, u := range a.Uses {
		fmt.Println("uses", u.Name, u.Value)
	}
	fmt.Println("loads", a.Loads)
	// Output:
	// 1:35 error: load "./lib/util.star": lib/util.star does not define thrice (did you mean twice?)
	// 3:5 error: unknown capability storage.artifact.gte (did you mean storage.artifact.get?)
	// 4:5 error: storage.artifact.get has no input "idd" (did you mean id?)
	// 4:5 error: storage.artifact.get requires input "id"
	// 6:5 error: unknown capability "storage.artefact.get" (did you mean storage.artifact.get?)
	// uses storage.artifact.get host payload
	// loads [lib/util.star]
}
