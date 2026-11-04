package types_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/craggy-io/script-services/types"
)

func parse(t *testing.T, src string) *types.Package {
	t.Helper()
	pkg, err := types.Parse("types.go", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return pkg
}

func schemaJSON(t *testing.T, pkg *types.Package, name string) string {
	t.Helper()
	s, err := pkg.Schema(name)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const kinds = `package api

import (
	"encoding/json"
	"time"
)

type Kinds struct {
	S   string            ` + "`json:\"s\"`" + `
	B   bool              ` + "`json:\"b\"`" + `
	I   int               ` + "`json:\"i\"`" + `
	I8  int8              ` + "`json:\"i8\"`" + `
	U16 uint16            ` + "`json:\"u16\"`" + `
	U   uint              ` + "`json:\"u\"`" + `
	F   float32           ` + "`json:\"f\"`" + `
	L   []string          ` + "`json:\"l\"`" + `
	A   [2]int            ` + "`json:\"a\"`" + `
	M   map[string]bool   ` + "`json:\"m\"`" + `
	P   *int              ` + "`json:\"p\"`" + `
	Any any               ` + "`json:\"any\"`" + `
	Ifc interface{}       ` + "`json:\"ifc\"`" + `
	Raw json.RawMessage   ` + "`json:\"raw\"`" + `
	T   time.Time         ` + "`json:\"t\"`" + `
	Bs  []byte            ` + "`json:\"bs\"`" + `
	N   Nested            ` + "`json:\"n\"`" + `
	Lv  Level             ` + "`json:\"lv\"`" + `
	Anon struct {
		X int ` + "`json:\"x\"`" + `
	} ` + "`json:\"anon\"`" + `
}

type Nested struct {
	Name string ` + "`json:\"name,omitempty\"`" + `
}

type Level string
`

func TestEveryKind(t *testing.T) {
	pkg := parse(t, kinds)
	s, err := pkg.Schema("Kinds")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"s":    `{"type":"string"}`,
		"b":    `{"type":"boolean"}`,
		"i":    `{"type":"integer"}`,
		"i8":   `{"type":"integer","minimum":-128,"maximum":127}`,
		"u16":  `{"type":"integer","minimum":0,"maximum":65535}`,
		"u":    `{"type":"integer","minimum":0}`,
		"f":    `{"type":"number"}`,
		"l":    `{"type":["null","array"],"items":{"type":"string"}}`,
		"a":    `{"type":"array","items":{"type":"integer"},"minItems":2,"maxItems":2}`,
		"m":    `{"type":"object","additionalProperties":{"type":"boolean"}}`,
		"p":    `{"type":["null","integer"]}`,
		"any":  `true`,
		"ifc":  `true`,
		"raw":  `true`,
		"t":    `{"type":"string","format":"date-time"}`,
		"bs":   `{"type":"string","description":"base64-encoded bytes","contentEncoding":"base64"}`,
		"n":    `{"type":"object","properties":{"name":{"type":"string"}},"additionalProperties":false}`,
		"lv":   `{"type":"string"}`,
		"anon": `{"type":"object","properties":{"x":{"type":"integer"}},"required":["x"],"additionalProperties":false}`,
	}
	for name, w := range want {
		b, _ := json.Marshal(s.Properties[name])
		if string(b) != w {
			t.Errorf("%s = %s, want %s", name, b, w)
		}
	}
	if got := strings.Join(s.PropertyOrder, ","); got != "s,b,i,i8,u16,u,f,l,a,m,p,any,ifc,raw,t,bs,n,lv,anon" {
		t.Errorf("order = %s", got)
	}
	// As in jsonschema.For, only omitempty/omitzero make a field optional:
	// the pointer p is required but nullable.
	if got := strings.Join(s.Required, ","); got != "s,b,i,i8,u16,u,f,l,a,m,p,any,ifc,raw,t,bs,n,lv,anon" {
		t.Errorf("required = %v", s.Required)
	}
	if b, _ := json.Marshal(s.AdditionalProperties); string(b) != "false" {
		t.Errorf("struct not closed: %s", b)
	}
	if got := strings.Join(pkg.Names(), ","); got != "Kinds,Level,Nested" {
		t.Errorf("Names = %s", got)
	}
}

// The shapes match jsonschema.For (as the registry used it) for the same
// Go types, so moving a type between Go and a declaration file keeps its
// schema.
type parityLeaf struct {
	Label string `json:"label" jsonschema:"a label"`
}

type parity struct {
	Name    string               `json:"name" jsonschema:"the name"`
	Count   int32                `json:"count,omitempty"`
	Ratio   float64              `json:"ratio"`
	Flags   []bool               `json:"flags"`
	Fixed   [3]uint8             `json:"fixed"`
	Lookup  map[string]int       `json:"lookup,omitempty"`
	Leaf    parityLeaf           `json:"leaf"`
	Leaves  []parityLeaf         `json:"leaves,omitzero"`
	MaybeP  *parityLeaf          `json:"maybe,omitempty"`
	Ptr     *int                 `json:"ptr"`
	PtrLeaf *parityLeaf          `json:"ptrLeaf"`
	Data    []byte               `json:"data"`
	Payload json.RawMessage      `json:"payload"`
	Any     any                  `json:"any"`
	Skip    string               `json:"-"`
	hidden  string               //nolint:unused
	NoTag   string               // no json tag: Go field name
	Nested  map[string][]float32 `json:"nested"`
}

const paritySrc = `package api

type parityLeaf struct {
	Label string ` + "`json:\"label\" jsonschema:\"a label\"`" + `
}

type parity struct {
	Name    string               ` + "`json:\"name\" jsonschema:\"the name\"`" + `
	Count   int32                ` + "`json:\"count,omitempty\"`" + `
	Ratio   float64              ` + "`json:\"ratio\"`" + `
	Flags   []bool               ` + "`json:\"flags\"`" + `
	Fixed   [3]uint8             ` + "`json:\"fixed\"`" + `
	Lookup  map[string]int       ` + "`json:\"lookup,omitempty\"`" + `
	Leaf    parityLeaf           ` + "`json:\"leaf\"`" + `
	Leaves  []parityLeaf         ` + "`json:\"leaves,omitzero\"`" + `
	MaybeP  *parityLeaf          ` + "`json:\"maybe,omitempty\"`" + `
	Ptr     *int                 ` + "`json:\"ptr\"`" + `
	PtrLeaf *parityLeaf          ` + "`json:\"ptrLeaf\"`" + `
	Data    []byte               ` + "`json:\"data\"`" + `
	Payload json.RawMessage      ` + "`json:\"payload\"`" + `
	Any     any                  ` + "`json:\"any\"`" + `
	Skip    string               ` + "`json:\"-\"`" + `
	hidden  string
	NoTag   string
	Nested  map[string][]float32 ` + "`json:\"nested\"`" + `
}
`

func TestParityWithJSONSchemaFor(t *testing.T) {
	want, err := jsonschema.For[parity](&jsonschema.ForOptions{TypeSchemas: map[reflect.Type]*jsonschema.Schema{
		reflect.TypeFor[json.RawMessage](): {},
		reflect.TypeFor[[]byte]():          {Type: "string", ContentEncoding: "base64"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	// Our []byte schema also says "base64" in its description.
	want.Properties["data"].Description = "base64-encoded bytes"
	wantJSON, _ := json.Marshal(want)
	pkg := parse(t, strings.Replace(paritySrc, "package api\n", "package api\n\nimport \"encoding/json\"\n", 1))
	if got := schemaJSON(t, pkg, "parity"); got != string(wantJSON) {
		t.Fatalf("schema differs from jsonschema.For:\n got %s\nwant %s", got, wantJSON)
	}
	var _ = parity{hidden: ""}
}

func TestDescriptionsAndEnums(t *testing.T) {
	pkg := parse(t, `package api

// Job describes one job.
//
// It has two paragraphs.
type Job struct {
	// ID identifies the job.
	ID string `+"`json:\"id\"`"+`
	Kind Kind `+"`json:\"kind\"`"+` // what sort
	Prio Prio `+"`json:\"prio,omitempty\" jsonschema:\"priority\"`"+`
	Blob []byte `+"`json:\"blob,omitempty\" jsonschema:\"raw payload\"`"+`
}

// Kind is a job kind.
// enum: build, "test", deploy
type Kind string

// enum: 1, 2, 3
type Prio int

type (
	// Ratio is grouped.
	// enum: 0.5, 1.5
	Ratio float64
)
`)
	got := schemaJSON(t, pkg, "Job")
	for _, want := range []string{
		`"description":"Job describes one job.\n\nIt has two paragraphs."`,
		`"id":{"type":"string","description":"ID identifies the job."}`,
		`"kind":{"type":"string","description":"what sort","enum":["build","test","deploy"]}`,
		`"prio":{"type":"integer","description":"priority","enum":[1,2,3]}`,
		`"blob":{"type":"string","description":"raw payload (base64-encoded bytes)","contentEncoding":"base64"}`,
		`"required":["id","kind"]`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("schema lacks %s\n%s", want, got)
		}
	}
	if got := schemaJSON(t, pkg, "Kind"); got != `{"type":"string","description":"Kind is a job kind.","enum":["build","test","deploy"]}` {
		t.Errorf("Kind = %s", got)
	}
	if got := schemaJSON(t, pkg, "Ratio"); got != `{"type":"number","description":"Ratio is grouped.","enum":[0.5,1.5]}` {
		t.Errorf("Ratio = %s", got)
	}
}

func TestEmbeddedStructsPromoteFields(t *testing.T) {
	pkg := parse(t, `package api

type Base struct {
	ID   string `+"`json:\"id\"`"+`
	Note string `+"`json:\"note,omitempty\"`"+`
}

type Item struct {
	Base
	Note int    `+"`json:\"note\"`"+`
	Name string `+"`json:\"name\"`"+`
}
`)
	got := schemaJSON(t, pkg, "Item")
	want := `{"type":"object","properties":{"id":{"type":"string"},"note":{"type":"integer"},"name":{"type":"string"}},"required":["id","note","name"],"additionalProperties":false}`
	if got != want {
		t.Fatalf("Item =\n%s\nwant\n%s", got, want)
	}
	// Properties follow field order, not declaration order in the file:
	// Late is declared after Early but embedded first.
	pkg = parse(t, `package api

type Early struct {
	Late
	A string `+"`json:\"a\"`"+`
}

type Late struct {
	B string `+"`json:\"b\"`"+`
}
`)
	if got := schemaJSON(t, pkg, "Early"); got != `{"type":"object","properties":{"b":{"type":"string"},"a":{"type":"string"}},"required":["b","a"],"additionalProperties":false}` {
		t.Fatalf("Early = %s", got)
	}
}

func TestErrorsCarryPositions(t *testing.T) {
	cases := []struct {
		name, src, typ, want string
	}{
		{"recursive", "package a\n\ntype Node struct {\n\tKids []Node `json:\"kids\"`\n}\n", "Node",
			"types.go:4:9: recursive type Node (Node -> Node)"},
		{"mutual", "package a\n\ntype A struct {\n\tB *B\n}\n\ntype B struct {\n\tA A\n}\n", "A",
			"types.go:8:4: recursive type A (A -> B -> A)"},
		{"unknown", "package a\n\ntype T struct {\n\tX Missing\n}\n", "T",
			"types.go:4:4: unknown type Missing"},
		{"chan", "package a\n\ntype T struct {\n\tC chan int\n}\n", "T",
			"types.go:4:4: unsupported type chan int"},
		{"func", "package a\n\ntype T func()\n", "T",
			"types.go:3:8: unsupported type func()"},
		{"mapkey", "package a\n\ntype T map[int]string\n", "T",
			"types.go:3:12: unsupported map key type int: keys must be strings"},
		{"foreign", "package a\n\nimport \"net/url\"\n\ntype T struct {\n\tU url.URL\n}\n", "T",
			"types.go:6:4: unsupported type url.URL"},
		{"notimported", "package a\n\ntype T struct {\n\tU time.Time\n}\n", "T",
			"types.go:4:4: unknown package time in time.Time (not imported)"},
		{"generic", "package a\n\ntype T[X any] struct{ V X }\n", "T",
			"types.go:3:6: generic type T is unsupported"},
		{"badenum", "package a\n\n// enum: one, two\ntype N int\n", "N",
			`types.go:3:1: type N: enum value "one" is not an integer`},
		{"structenum", "package a\n\n// enum: a\ntype S struct{}\n", "S",
			"types.go:3:1: type S: enum needs a string, integer, number or bool type"},
		{"method", "package a\n\ntype T interface{ M() }\n", "T",
			"types.go:3:8: unsupported type interface{M()}: only empty interfaces are allowed"},
		{"dup", "package a\n\ntype T struct {\n\tA string `json:\"x\"`\n\tB string `json:\"x\"`\n}\n", "T",
			`types.go:5:2: duplicate JSON field name "x"`},
		{"emptytag", "package a\n\ntype T struct {\n\tA string `jsonschema:\"\"`\n}\n", "T",
			"types.go:4:11: empty jsonschema tag on field A"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pkg := parse(t, tc.src)
			_, err := pkg.Schema(tc.typ)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v\nwant  %s", err, tc.want)
			}
			var te *types.Error
			if !errors.As(err, &te) || te.Pos.Line == 0 {
				t.Fatalf("err %v is not a positioned *types.Error", err)
			}
		})
	}
	if _, err := parse(t, "package a\n").Schema("Nope"); err == nil || !strings.Contains(err.Error(), `unknown type "Nope"`) {
		t.Fatalf("unknown name err = %v", err)
	}
}

func TestParseRequiresGoFile(t *testing.T) {
	if _, err := types.Parse("x.go", []byte("type T struct{}\n")); err == nil || !strings.Contains(err.Error(), "x.go:1:1") {
		t.Fatalf("no package clause err = %v", err)
	}
	if _, err := types.Parse("x.go", []byte("package a\ntype T struct{\n")); err == nil {
		t.Fatal("syntax error accepted")
	}
}

func TestTimeDurationAndSchemasAreFresh(t *testing.T) {
	pkg := parse(t, "package a\n\nimport t \"time\"\n\ntype D struct {\n\tWait t.Duration `json:\"wait\"`\n\tAt *t.Time `json:\"at\"`\n}\n")
	got := schemaJSON(t, pkg, "D")
	if !strings.Contains(got, `"wait":{"type":"integer","description":"duration in nanoseconds"}`) ||
		!strings.Contains(got, `"at":{"type":["null","string"],"format":"date-time"}`) {
		t.Fatalf("D = %s", got)
	}
	a, _ := pkg.Schema("D")
	a.Properties["wait"].Type = "string"
	if got2 := schemaJSON(t, pkg, "D"); got2 != got {
		t.Fatal("Schema results share state")
	}
	var _ = time.Second
}

// A pointer to a named type with an enum admits null as well as the enum's
// values.
func TestPointerToEnumAdmitsNull(t *testing.T) {
	pkg := parse(t, `package api

type Cfg struct {
	Level *Level `+"`json:\"level\"`"+`
}

// enum: low, high
type Level string
`)
	s, err := pkg.Schema("Cfg")
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	for doc, ok := range map[string]bool{
		`{"level": null}`:  true,
		`{"level": "low"}`: true,
		`{"level": "mid"}`: false,
		`{"level": 1}`:     false,
	} {
		var v any
		if err := json.Unmarshal([]byte(doc), &v); err != nil {
			t.Fatal(err)
		}
		if err := res.Validate(v); (err == nil) != ok {
			t.Errorf("Validate(%s) = %v, want ok=%v", doc, err, ok)
		}
	}
}

// Integer enum values are decimal: a leading zero is not octal and a 0x
// prefix is not hex.
func TestIntegerEnumsAreDecimal(t *testing.T) {
	pkg := parse(t, "package a\n\n// enum: 010, 7, -3\ntype N int\n\n// enum: 0x1f\ntype H int\n")
	if got := schemaJSON(t, pkg, "N"); got != `{"type":"integer","enum":[10,7,-3]}` {
		t.Fatalf("N = %s", got)
	}
	if _, err := pkg.Schema("H"); err == nil || !strings.Contains(err.Error(), `enum value "0x1f" is not an integer`) {
		t.Fatalf("hex enum err = %v", err)
	}
}

// Go types mirroring the declarations below, to pin encoding/json's rule
// (embTwoTags is left out: go vet rejects its repeated tag, though
// encoding/json drops both fields the same way).
type (
	embTagged struct {
		X string `json:"Name"`
	}
	embUntagged struct{ Name int }
	embOther    struct{ Name bool }
	embOneTag   struct {
		embTagged
		embUntagged
		ID string `json:"id"`
	}
	embNoTags struct {
		embUntagged
		embOther
		ID string `json:"id"`
	}
)

// Promoted fields at the same depth with the same JSON name follow
// encoding/json: exactly one tagged field wins; otherwise all are dropped.
func TestEmbeddedSameDepthFollowsEncodingJSON(t *testing.T) {
	pkg := parse(t, `package api

type embTagged struct{ X string `+"`json:\"Name\"`"+` }
type embUntagged struct{ Name int }
type embOther struct{ Name bool }
type embTagged2 struct{ Y string `+"`json:\"Name\"`"+` }

type embOneTag struct {
	embTagged
	embUntagged
	ID string `+"`json:\"id\"`"+`
}

type embNoTags struct {
	embUntagged
	embOther
	ID string `+"`json:\"id\"`"+`
}

type embTwoTags struct {
	embTagged
	embTagged2
	ID string `+"`json:\"id\"`"+`
}
`)
	// What encoding/json does with the same types.
	for v, want := range map[any]string{
		embOneTag{embTagged: embTagged{X: "x"}, embUntagged: embUntagged{Name: 1}}: `{"Name":"x","id":""}`,
		embNoTags{}: `{"id":""}`,
	} {
		if b, _ := json.Marshal(v); string(b) != want {
			t.Fatalf("encoding/json %T = %s, want %s", v, b, want)
		}
	}
	cases := map[string]string{
		"embOneTag":  `{"type":"object","properties":{"Name":{"type":"string"},"id":{"type":"string"}},"required":["Name","id"],"additionalProperties":false}`,
		"embNoTags":  `{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`,
		"embTwoTags": `{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`,
	}
	for name, want := range cases {
		if got := schemaJSON(t, pkg, name); got != want {
			t.Errorf("%s =\n%s\nwant\n%s", name, got, want)
		}
	}
}
