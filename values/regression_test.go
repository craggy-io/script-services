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

// A self-referential list or dict used to overflow the Go stack fatally;
// it must now be an ordinary error.
func TestToGoCyclicValues(t *testing.T) {
	l := starlark.NewList(nil)
	if err := l.Append(l); err != nil {
		t.Fatal(err)
	}
	if _, err := values.ToGo(l); err == nil || !strings.Contains(err.Error(), "cyclic") {
		t.Fatalf("cyclic list err = %v", err)
	}
	d := starlark.NewDict(1)
	if err := d.SetKey(starlark.String("self"), d); err != nil {
		t.Fatal(err)
	}
	if _, err := values.ToJSON(d); err == nil || !strings.Contains(err.Error(), "cyclic") {
		t.Fatalf("cyclic dict err = %v", err)
	}
	// Shared but acyclic substructure still converts.
	sub := starlark.NewList([]starlark.Value{starlark.MakeInt(1)})
	raw, err := values.ToJSON(starlark.Tuple{sub, sub})
	if err != nil || string(raw) != `[[1],[1]]` {
		t.Fatalf("shared sublist = %s, %v", raw, err)
	}
}

func TestFromGoCyclicValues(t *testing.T) {
	m := map[string]any{}
	m["self"] = m
	if _, err := values.FromGo(m); err == nil || !strings.Contains(err.Error(), "cyclic") {
		t.Fatalf("cyclic map err = %v", err)
	}
	shared := map[string]any{"n": 1.0}
	if _, err := values.FromGo(map[string]any{"a": shared, "b": shared}); err != nil {
		t.Fatalf("shared map err = %v", err)
	}
}

func TestFromJSONTrailingAndWhitespace(t *testing.T) {
	for _, raw := range []string{`1 2`, `{"a":1} garbage`, `{"a":1} {"b":2}`} {
		if _, err := values.FromJSON(json.RawMessage(raw)); err == nil {
			t.Fatalf("FromJSON(%q) accepted", raw)
		}
	}
	if v, err := values.FromJSON(json.RawMessage("  \n ")); err != nil || v != starlark.None {
		t.Fatalf("whitespace = %v, %v", v, err)
	}
}

func TestFromJSONBigIntegersStayExact(t *testing.T) {
	v, err := values.FromJSON(json.RawMessage(`123456789012345678901234567890`))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := v.(starlark.Int); !ok || v.String() != "123456789012345678901234567890" {
		t.Fatalf("big int = %s (%s)", v, v.Type())
	}
	v, err = values.FromGo(uint64(math.MaxUint64))
	if err != nil || v.String() != "18446744073709551615" {
		t.Fatalf("uint64 max = %v, %v", v, err)
	}
}

func TestToGoNonFinite(t *testing.T) {
	if _, err := values.ToGo(starlark.Float(math.Inf(1))); err == nil {
		t.Fatal("ToGo(+Inf) accepted")
	}
	if _, err := values.ToGo(starlark.Float(math.NaN())); err == nil {
		t.Fatal("ToGo(NaN) accepted")
	}
	huge := starlark.MakeBigInt(new(big.Int).Lsh(big.NewInt(1), 2000))
	if _, err := values.ToGo(huge); err == nil {
		t.Fatal("ToGo(2^2000) accepted")
	}
}
