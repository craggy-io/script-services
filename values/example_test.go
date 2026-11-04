package values_test

import (
	"encoding/json"
	"fmt"

	"go.starlark.net/starlark"

	"github.com/craggy-io/script-services/dialect"
	"github.com/craggy-io/script-services/values"
)

// JSON objects become dicts with sorted keys; only integer spellings become
// ints. The round trip back to JSON follows the same rules.
func ExampleFromJSON() {
	v, err := values.FromJSON(json.RawMessage(`{"b": [1, 2.5, 1e3], "a": null}`))
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(v)
	raw, err := values.ToJSON(v)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(string(raw))
	// Output:
	// {"a": None, "b": [1, 2.5, 1000.0]}
	// {"a":null,"b":[1,2.5,1000]}
}

// Only JSON-shaped values convert back: a function is an error.
func ExampleToGo() {
	tuple := starlark.Tuple{starlark.MakeInt(1), starlark.String("x")}
	gv, err := values.ToGo(tuple)
	fmt.Printf("%#v %v\n", gv, err)
	_, err = values.ToGo(starlark.NewBuiltin("f", nil))
	fmt.Println(err)
	// Output:
	// []interface {}{1, "x"} <nil>
	// value of type builtin_function_or_method is not JSON-convertible
}

// A host built-in turns its arguments into one JSON object: keywords on top
// of an optional positional dict.
func ExampleKwargsToJSON() {
	get := starlark.NewBuiltin("get", func(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		raw, err := values.KwargsToJSON(b.Name(), args, kwargs)
		if err != nil {
			return nil, err
		}
		return starlark.String(raw), nil
	})
	thread := &starlark.Thread{}
	for _, src := range []string{`get(id = "a", depth = 2)`, `get({"id": "a"}, depth = 3)`, `get(1, 2)`} {
		v, err := starlark.EvalOptions(dialect.FileOptions, thread, "x.star", src, starlark.StringDict{"get": get})
		if err != nil {
			fmt.Println(err)
			continue
		}
		fmt.Println(v)
	}
	// Output:
	// "{\"depth\":2,\"id\":\"a\"}"
	// "{\"depth\":3,\"id\":\"a\"}"
	// get: pass arguments as keywords or one dict
}
