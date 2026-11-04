package schema_test

import (
	"strings"
	"testing"

	"go.starlark.net/starlark"

	"github.com/craggy-io/script-services/schema"
)

// A cyclic field dict used to overflow the Go stack fatally.
func TestOfCyclicDict(t *testing.T) {
	d := starlark.NewDict(1)
	if err := d.SetKey(starlark.String("self"), d); err != nil {
		t.Fatal(err)
	}
	if _, err := schema.Of(d); err == nil || !strings.Contains(err.Error(), "cyclic") {
		t.Fatalf("cyclic dict err = %v", err)
	}
	g := eval(t, "d = {}\nd[\"self\"] = d\n")
	if _, err := schema.Of(g["d"]); err == nil || !strings.Contains(err.Error(), "cyclic") {
		t.Fatalf("script cyclic dict err = %v", err)
	}
}

// schema.raw is verbatim: keyword-only schemas are not reinterpreted as
// field dicts.
func TestRawVerbatim(t *testing.T) {
	g := eval(t, `
c = schema.raw({"const": 42})
n = schema.raw({"not": {"type": "string"}})
f = schema.raw({"type": "string", "format": "email"})
`)
	if got := jsonOf(of(t, g["c"])); got != `{"const":42}` {
		t.Fatalf("const raw = %s", got)
	}
	if got := jsonOf(of(t, g["n"])); got != `{"not":{"type":"string"}}` {
		t.Fatalf("not raw = %s", got)
	}
	if got := jsonOf(of(t, g["f"])); got != `{"type":"string","format":"email"}` {
		t.Fatalf("format raw = %s", got)
	}
	if err := validate(t, of(t, g["c"]), `42`); err != nil {
		t.Fatalf("const schema rejects 42: %v", err)
	}
	if err := validate(t, of(t, g["c"]), `41`); err == nil {
		t.Fatal("const schema accepts 41")
	}
}
