# Values

[Project](../README.md) · [Documentation index](README.md)

Available Go API: `github.com/craggy-io/script-services/values`.

## Purpose

Convert between Starlark values, Go values and JSON with one fixed set of rules,
so every host built-in and every script sees the same value shapes.

## Features

- JSON to Starlark: objects become dicts with sorted keys, arrays become lists.
- Integer-spelled JSON numbers become ints; other numbers become floats.
- Starlark to JSON for JSON-shaped values only; functions, modules and other host
  values are errors.
- `KwargsToJSON` turns a built-in's arguments into one JSON object.

## Mental model

There is one value model: JSON. `script.Exec` uses it for `input` and for the
entry function's result, `schema` uses it for raw schema dicts, and host built-ins
use it for their arguments and results. A value that cannot become JSON cannot
cross the host boundary.

## Responsibilities and boundaries

`values` converts; it does not validate against schemas or know about built-ins
beyond formatting their argument errors.

Starlark → Go (`ToGo`) produces `nil`, `bool`, `string`, `int64`, `float64`,
`[]any` or `map[string]any`:

| Starlark | Go / JSON |
| --- | --- |
| `None` | `nil` / `null` |
| `bool`, `string`, `float` | `bool`, `string`, `float64` |
| `int` | `int64`; too large for int64 → `float64` |
| `list`, `tuple`, `set` | `[]any` (set in iteration order) |
| `dict` with string keys | `map[string]any`; a non-string key is an error |
| anything else | error "value of type T is not JSON-convertible" |

Go → Starlark (`FromGo`): decoded-JSON shapes (`nil`, `bool`, `string`,
`json.Number`, `float64`, `[]any`, `map[string]any`) convert directly; anything
else is marshalled with `encoding/json` and converted from that JSON. A `float64`
with an integral value in int64's range becomes an int.

JSON → Starlark (`FromJSON`) decodes with `UseNumber`, so only numbers spelled as
integers (`3`, not `3.0` or `1e3`) become ints; integer spellings beyond int64
stay exact ints (Starlark ints are arbitrary precision). Empty or whitespace-only
input is `None`, and anything after the first JSON value is an error.

## Public interfaces

- `FromJSON(raw json.RawMessage) (starlark.Value, error)`
- `FromGo(v any) (starlark.Value, error)`
- `ToGo(v starlark.Value) (any, error)`
- `ToJSON(v starlark.Value) (json.RawMessage, error)`
- `KwargsToJSON(fn string, args starlark.Tuple, kwargs []starlark.Tuple) (json.RawMessage, error)`

## Semantics, errors, and limits

- `ToJSON` marshals `ToGo`'s result, so object keys come out sorted.
- `KwargsToJSON` accepts keyword arguments, optionally on top of one positional
  dict (keywords win). A non-dict positional argument fails with
  "fn: single positional argument must be a dict, got T"; two or more positionals
  fail with "fn: pass arguments as keywords or one dict".
- Ints beyond int64 lose precision when converted to `float64`; ints beyond
  float64's range are errors rather than `+Inf`.
- `FromGo` turns `±Inf` floats into Starlark floats (`+inf`); converting back is an
  error, as NaN and ±Inf have no JSON form.
- Cyclic values (a list or dict containing itself, a cyclic Go `map`/`slice`) and
  values nested past `encoding/json`'s limit are errors in both directions.
- A `float64` becomes an int only inside int64's range, `[-2^63, 2^63)`:
  `-(1<<63)` is an int, but `float64(1<<63)` (which is also what
  `float64(math.MaxInt64)` rounds to) stays a float
  (`TestFromGoFloatIntBoundaries`).

## Examples

From [example_test.go](../values/example_test.go):

```go
v, _ := values.FromJSON(json.RawMessage(`{"b": [1, 2.5, 1e3], "a": null}`))
fmt.Println(v)                 // {"a": None, "b": [1, 2.5, 1000.0]}
raw, _ := values.ToJSON(v)     // {"a":null,"b":[1,2.5,1000]}
```

A host built-in in the style used throughout the tests (`ExampleKwargsToJSON`):

```go
get := starlark.NewBuiltin("get", func(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	raw, err := values.KwargsToJSON(b.Name(), args, kwargs)
	if err != nil {
		return nil, err
	}
	var in struct{ ID string }
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	return values.FromGo(map[string]any{"got": in.ID})
})
```

`get(id = "a")` and `get({"id": "a"})` both reach the built-in as `{"id":"a"}`.
