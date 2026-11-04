package values_test

import (
	"encoding/json"
	"math"
	"math/big"
	"strings"
	"testing"

	"go.starlark.net/starlark"

	"github.com/craggy-io/script-services/values"
)

func TestFromJSONNumbersAndShapes(t *testing.T) {
	v, err := values.FromJSON(json.RawMessage(`{"b": 2, "a": [1.5, 3, null, true, "s"], "big": 1e3}`))
	if err != nil {
		t.Fatal(err)
	}
	d := v.(*starlark.Dict)
	if got := d.Keys(); len(got) != 3 || got[0] != starlark.String("a") || got[1] != starlark.String("b") {
		t.Fatalf("keys not sorted: %v", got)
	}
	b, _, _ := d.Get(starlark.String("b"))
	if _, ok := b.(starlark.Int); !ok {
		t.Fatalf("integral number is %s, want int", b.Type())
	}
	big, _, _ := d.Get(starlark.String("big"))
	// As before: only numbers spelled as integers become ints.
	if _, ok := big.(starlark.Float); !ok {
		t.Fatalf("1e3 is %s, want float", big.Type())
	}
	a, _, _ := d.Get(starlark.String("a"))
	if a.String() != `[1.5, 3, None, True, "s"]` {
		t.Fatalf("list = %s", a)
	}
	if v, err := values.FromJSON(nil); err != nil || v != starlark.None {
		t.Fatalf("empty = %v, %v", v, err)
	}
	if _, err := values.FromJSON(json.RawMessage(`{`)); err == nil {
		t.Fatal("bad JSON accepted")
	}
}

func TestFromGo(t *testing.T) {
	type pt struct {
		X int     `json:"x"`
		Y float64 `json:"y"`
	}
	v, err := values.FromGo(map[string]any{"p": pt{X: 1, Y: 2.5}, "f": 4.0, "inf": math.Inf(1)})
	if err != nil {
		t.Fatal(err)
	}
	if v.String() != `{"f": 4, "inf": +inf, "p": {"x": 1, "y": 2.5}}` {
		t.Fatalf("FromGo = %s", v)
	}
	if _, err := values.FromGo(make(chan int)); err == nil {
		t.Fatal("channel accepted")
	}
}

func TestToGoAndToJSON(t *testing.T) {
	set := starlark.NewSet(1)
	_ = set.Insert(starlark.String("only"))
	d := starlark.NewDict(4)
	_ = d.SetKey(starlark.String("tuple"), starlark.Tuple{starlark.MakeInt(1), starlark.Float(2.5)})
	_ = d.SetKey(starlark.String("set"), set)
	_ = d.SetKey(starlark.String("huge"), starlark.MakeBigInt(new(big.Int).Lsh(big.NewInt(1), 70)))
	_ = d.SetKey(starlark.String("none"), starlark.None)
	raw, err := values.ToJSON(d)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"huge":1.1805916207174113e+21,"none":null,"set":["only"],"tuple":[1,2.5]}` {
		t.Fatalf("ToJSON = %s", raw)
	}
	bad := starlark.NewDict(1)
	_ = bad.SetKey(starlark.MakeInt(1), starlark.None)
	if _, err := values.ToGo(bad); err == nil || !strings.Contains(err.Error(), "not a string") {
		t.Fatalf("int key err = %v", err)
	}
	fn := starlark.NewBuiltin("f", nil)
	if _, err := values.ToJSON(starlark.NewList([]starlark.Value{fn})); err == nil || !strings.Contains(err.Error(), "not JSON-convertible") {
		t.Fatalf("function err = %v", err)
	}
}

func TestRoundTrip(t *testing.T) {
	in := `{"a":[1,2.5,"x",{"n":null}],"b":true}`
	v, err := values.FromJSON(json.RawMessage(in))
	if err != nil {
		t.Fatal(err)
	}
	out, err := values.ToJSON(v)
	if err != nil || string(out) != in {
		t.Fatalf("round trip = %s, %v", out, err)
	}
}

func TestKwargsToJSON(t *testing.T) {
	d := starlark.NewDict(1)
	_ = d.SetKey(starlark.String("id"), starlark.String("a"))
	_ = d.SetKey(starlark.String("n"), starlark.MakeInt(1))
	kw := []starlark.Tuple{{starlark.String("n"), starlark.MakeInt(2)}}
	raw, err := values.KwargsToJSON("f", starlark.Tuple{d}, kw)
	if err != nil || string(raw) != `{"id":"a","n":2}` {
		t.Fatalf("dict+kwargs = %s, %v", raw, err)
	}
	if raw, err := values.KwargsToJSON("f", nil, nil); err != nil || string(raw) != `{}` {
		t.Fatalf("empty = %s, %v", raw, err)
	}
	if _, err := values.KwargsToJSON("f", starlark.Tuple{starlark.MakeInt(1)}, nil); err == nil || !strings.Contains(err.Error(), "f: single positional argument must be a dict, got int") {
		t.Fatalf("non-dict err = %v", err)
	}
	if _, err := values.KwargsToJSON("f", starlark.Tuple{d, d}, nil); err == nil || !strings.Contains(err.Error(), "keywords or one dict") {
		t.Fatalf("two positionals err = %v", err)
	}
}

// Floats convert to ints only when int64 holds them: 2^63 does not.
func TestFromGoFloatIntBoundaries(t *testing.T) {
	cases := []struct {
		in   float64
		want string // Starlark type and value
	}{
		{-(1 << 63), "int -9223372036854775808"},
		{1 << 62, "int 4611686018427387904"},
		{math.Nextafter(1<<63, 0), "int 9223372036854774784"},
		{1 << 63, "float 9.223372036854776e+18"},
		{1 << 64, "float 1.8446744073709552e+19"},
		{math.Nextafter(-(1 << 63), math.Inf(-1)), "float -9.223372036854778e+18"},
		{-0.0, "int 0"},
	}
	for _, c := range cases {
		v, err := values.FromGo(c.in)
		if err != nil {
			t.Fatal(err)
		}
		if got := v.Type() + " " + v.String(); got != c.want {
			t.Errorf("FromGo(%v) = %s, want %s", c.in, got, c.want)
		}
	}
}
