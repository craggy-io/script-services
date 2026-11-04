package schema_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"go.starlark.net/starlark"

	"github.com/craggy-io/script-services/dialect"
	"github.com/craggy-io/script-services/schema"
	"github.com/craggy-io/script-services/types"
)

// eval runs src with the schema module and returns its globals.
func eval(t *testing.T, src string, opts ...schema.Option) starlark.StringDict {
	t.Helper()
	pre := starlark.StringDict{"schema": schema.Module(opts...)}
	prog, err := dialect.Compile("s.star", []byte(src), pre.Has)
	if err != nil {
		t.Fatal(err)
	}
	g, err := prog.Init(&starlark.Thread{}, pre)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func of(t *testing.T, v starlark.Value, opts ...schema.Option) *jsonschema.Schema {
	t.Helper()
	s, err := schema.Of(v, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func validate(t *testing.T, s *jsonschema.Schema, doc string) error {
	t.Helper()
	res, err := s.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal([]byte(doc), &v); err != nil {
		t.Fatal(err)
	}
	return res.Validate(v)
}

func jsonOf(s *jsonschema.Schema) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestFieldDictSchema(t *testing.T) {
	g := eval(t, `in_ = {"artifact": schema.string(required = True, description = "artifact id"), "depth": schema.int(), "tags": schema.list(items = schema.string())}`)
	s := of(t, g["in_"])
	got := jsonOf(s)
	for _, want := range []string{`"required":["artifact"]`, `"artifact id"`, `"additionalProperties":false`, `"items":{"type":"string"}`} {
		if !strings.Contains(got, want) {
			t.Fatalf("schema %s lacks %s", got, want)
		}
	}
	if err := validate(t, s, `{"artifact":"a","depth":2}`); err != nil {
		t.Fatal(err)
	}
	if err := validate(t, s, `{"depth":2}`); err == nil {
		t.Fatal("missing required field accepted")
	}
	if err := validate(t, s, `{"artifact":"a","extra":1}`); err == nil {
		t.Fatal("closed object accepted an extra field")
	}
}

// Ported from the registry's TestLoadRawAndDictSchemas.
func TestRawAndNestedObjectSchemas(t *testing.T) {
	g := eval(t, `
raw = {"type": "object", "properties": {"n": {"type": "integer", "minimum": 2}}, "required": ["n"]}
obj = schema.object({"cfg": schema.object({"level": schema.integer(required = True)}, required = True)})
wrapped = schema.raw('{"type": "string", "maxLength": 3}')
open_ = schema.object()
`)
	raw := of(t, g["raw"])
	if err := validate(t, raw, `{"n":21}`); err != nil {
		t.Fatal(err)
	}
	if err := validate(t, raw, `{"n":1}`); err == nil {
		t.Fatal("raw minimum not enforced")
	}
	obj := of(t, g["obj"])
	if err := validate(t, obj, `{"cfg":{"level":3}}`); err != nil {
		t.Fatal(err)
	}
	if err := validate(t, obj, `{}`); err == nil {
		t.Fatal("nested required not enforced")
	}
	if err := validate(t, of(t, g["wrapped"]), `"abcd"`); err == nil {
		t.Fatal("schema.raw string not enforced")
	}
	if err := validate(t, of(t, g["open_"]), `{"anything":1}`); err != nil {
		t.Fatalf("schema.object() with no fields should stay open: %v", err)
	}
}

func TestOfInputForms(t *testing.T) {
	if s := of(t, starlark.String(`{"type":"integer"}`)); s.Type != "integer" {
		t.Fatalf("JSON string = %s", jsonOf(s))
	}
	if s := of(t, starlark.String(`true`)); jsonOf(s) != `true` {
		t.Fatalf("true = %s", jsonOf(s))
	}
	if s := of(t, starlark.String(`false`)); jsonOf(s) != `false` {
		t.Fatalf("false = %s", jsonOf(s))
	}
	if s := of(t, starlark.None); s != nil {
		t.Fatalf("None = %v", s)
	}
	for _, bad := range []starlark.Value{starlark.String("{not json"), starlark.MakeInt(3), starlark.String(`{"$ref":"#/nope"}`)} {
		if _, err := schema.Of(bad); err == nil {
			t.Fatalf("Of(%s) succeeded", bad)
		}
	}
	d := starlark.NewDict(1)
	_ = d.SetKey(starlark.MakeInt(1), starlark.None)
	if _, err := schema.Of(d); err == nil || !strings.Contains(err.Error(), "not a string") {
		t.Fatalf("int field name err = %v", err)
	}
}

const decls = `package api

// DecompileIn is what a decompiler takes.
type DecompileIn struct {
	Artifact string ` + "`json:\"artifact\" jsonschema:\"artifact id\"`" + `
	Level    Level  ` + "`json:\"level,omitempty\"`" + `
}

// Level is an effort level.
// enum: fast, full
type Level string
`

func TestWithTypes(t *testing.T) {
	pkg, err := types.Parse("api.go", []byte(decls))
	if err != nil {
		t.Fatal(err)
	}
	opt := schema.WithTypes(pkg)
	g := eval(t, `
t = schema.type("DecompileIn")
fields = {"in": schema.type("DecompileIn", required = True), "level": schema.type("Level", description = "how hard")}
`, opt)
	s := of(t, g["t"], opt)
	if err := validate(t, s, `{"artifact":"a","level":"full"}`); err != nil {
		t.Fatal(err)
	}
	if err := validate(t, s, `{"artifact":"a","level":"slow"}`); err == nil {
		t.Fatal("enum not enforced")
	}
	f := of(t, g["fields"], opt)
	if len(f.Required) != 1 || f.Required[0] != "in" || f.Properties["level"].Description != "how hard" {
		t.Fatalf("fields = %s", jsonOf(f))
	}
	if s := of(t, starlark.String("Level"), opt); len(s.Enum) != 2 {
		t.Fatalf("type name string = %s", jsonOf(s))
	}
	// Without WithTypes there is no schema.type, and a name is not JSON.
	pre := starlark.StringDict{"schema": schema.Module()}
	if _, err := dialect.Compile("x.star", []byte(`x = schema.type("Level")`), pre.Has); err != nil {
		t.Fatal(err)
	}
	prog, _ := dialect.Compile("x.star", []byte(`x = schema.type("Level")`), pre.Has)
	if _, err := prog.Init(&starlark.Thread{}, pre); err == nil || !strings.Contains(err.Error(), "no .type field") {
		t.Fatalf("schema.type without WithTypes: %v", err)
	}
	if _, err := schema.Of(starlark.String("Level")); err == nil {
		t.Fatal("type name accepted without WithTypes")
	}
	prog, _ = dialect.Compile("y.star", []byte(`x = schema.type("Nope")`), pre.Has)
	pre2 := starlark.StringDict{"schema": schema.Module(opt)}
	prog, _ = dialect.Compile("y.star", []byte(`x = schema.type("Nope")`), pre2.Has)
	if _, err := prog.Init(&starlark.Thread{}, pre2); err == nil || !strings.Contains(err.Error(), `unknown type "Nope"`) {
		t.Fatalf("unknown type err = %v", err)
	}
}

func TestValueString(t *testing.T) {
	g := eval(t, `s = schema.string(description = "d")`)
	v := g["s"].(*schema.Value)
	if v.String() != `schema({"type":"string","description":"d"})` || v.Type() != "schema" {
		t.Fatalf("String = %s", v)
	}
	if _, err := v.Hash(); err == nil {
		t.Fatal("schema values should be unhashable")
	}
}

// A field dict may have fields named like schema keywords: any schema.*
// value among its values makes it a field dict.
func TestFieldNamedLikeKeyword(t *testing.T) {
	g := eval(t, `fields = {"type": schema.string(required = True), "name": schema.string(), "description": schema.int()}`)
	s := of(t, g["fields"])
	if got := jsonOf(s); got != `{"type":"object","properties":{"type":{"type":"string"},"name":{"type":"string"},"description":{"type":"integer"}},"required":["type"],"additionalProperties":false}` {
		t.Fatalf("schema = %s", got)
	}
	if err := validate(t, s, `{"type": "x", "name": "y", "description": 1}`); err != nil {
		t.Fatal(err)
	}
	// A dict of plain values with a keyword is still raw JSON Schema.
	g = eval(t, `raw = {"type": "string", "description": "d"}`)
	if got := jsonOf(of(t, g["raw"])); got != `{"type":"string","description":"d"}` {
		t.Fatalf("raw = %s", got)
	}
}

// Of returns a copy: changing it does not change the script's value.
func TestOfReturnsACopy(t *testing.T) {
	g := eval(t, `s = schema.object({"z": schema.string(required = True), "a": schema.list(items = schema.object({"y": schema.int(), "b": schema.int()}))})`)
	want := `{"type":"object","properties":{"z":{"type":"string"},"a":{"type":"array","items":{"type":"object","properties":{"y":{"type":"integer"},"b":{"type":"integer"}},"additionalProperties":false}}},"required":["z"],"additionalProperties":false}`
	first := of(t, g["s"])
	if got := jsonOf(first); got != want {
		t.Fatalf("first Of = %s", got) // property order survives the copy
	}
	first.Description = "changed"
	first.Required[0] = "changed"
	first.Properties["z"].Type = "integer"
	first.Properties["a"].Items.Properties["y"].Type = "string"
	first.Properties["b"] = &jsonschema.Schema{}
	if got := jsonOf(of(t, g["s"])); got != want {
		t.Fatalf("second Of = %s", got)
	}
}
