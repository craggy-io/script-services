package schema_test

import (
	"encoding/json"
	"fmt"

	"go.starlark.net/starlark"

	"github.com/craggy-io/script-services/dialect"
	"github.com/craggy-io/script-services/schema"
	"github.com/craggy-io/script-services/types"
)

// A script declares its inputs with the schema module; the host converts
// the declaration with Of. A field dict becomes a closed object whose
// required list comes from required = True.
func ExampleOf() {
	predeclared := starlark.StringDict{"schema": schema.Module()}
	src := `
INPUT = {
    "artifact": schema.string(required = True, description = "artifact id"),
    "depth": schema.int(),
    "tags": schema.list(items = schema.string()),
}
`
	prog, err := dialect.Compile("tool.star", []byte(src), predeclared.Has)
	if err != nil {
		fmt.Println(err)
		return
	}
	globals, err := prog.Init(&starlark.Thread{}, predeclared)
	if err != nil {
		fmt.Println(err)
		return
	}
	s, err := schema.Of(globals["INPUT"])
	if err != nil {
		fmt.Println(err)
		return
	}
	out, _ := json.Marshal(s)
	fmt.Println(string(out))
	// Output:
	// {"type":"object","properties":{"artifact":{"type":"string","description":"artifact id"},"depth":{"type":"integer"},"tags":{"type":"array","items":{"type":"string"}}},"required":["artifact"],"additionalProperties":false}
}

// WithTypes connects the schema module to a Go-syntax declaration file:
// schema.type("Name") in scripts, and declared names accepted by Of.
func ExampleWithTypes() {
	pkg, err := types.Parse("types.go", []byte(`package api

// Level is an effort level.
// enum: fast, full
type Level string
`))
	if err != nil {
		fmt.Println(err)
		return
	}
	opt := schema.WithTypes(pkg)
	predeclared := starlark.StringDict{"schema": schema.Module(opt)}
	prog, err := dialect.Compile("tool.star", []byte(`OUT = {"level": schema.type("Level", required = True)}`), predeclared.Has)
	if err != nil {
		fmt.Println(err)
		return
	}
	globals, err := prog.Init(&starlark.Thread{}, predeclared)
	if err != nil {
		fmt.Println(err)
		return
	}
	out, _ := schema.Of(globals["OUT"], opt)
	b, _ := json.Marshal(out)
	fmt.Println(string(b))
	byName, _ := schema.Of(starlark.String("Level"), opt)
	b, _ = json.Marshal(byName)
	fmt.Println(string(b))
	// Output:
	// {"type":"object","properties":{"level":{"type":"string","description":"Level is an effort level.","enum":["fast","full"]}},"required":["level"],"additionalProperties":false}
	// {"type":"string","description":"Level is an effort level.","enum":["fast","full"]}
}

// Parse accepts JSON Schema documents, including the boolean schemas, and
// rejects ones that do not resolve.
func ExampleParse() {
	for _, doc := range []string{`{"type":"string","maxLength":3}`, `false`, `{"$ref":"#/nope"}`} {
		s, err := schema.Parse(json.RawMessage(doc))
		if err != nil {
			fmt.Println("error")
			continue
		}
		b, _ := json.Marshal(s)
		fmt.Println(string(b))
	}
	// Output:
	// {"type":"string","maxLength":3}
	// false
	// error
}
