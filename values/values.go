// Package values converts between Starlark values, Go values and JSON with
// one fixed set of rules, so every host built-in and every script sees the
// same value shapes:
//
//   - JSON objects become dicts with sorted keys, arrays become lists;
//   - integral JSON numbers become ints, other numbers floats;
//   - only JSON-shaped Starlark values (None, bools, strings, ints, floats,
//     lists, tuples, dicts with string keys and sets) convert back; functions,
//     modules and other host values are errors.
package values

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"

	"go.starlark.net/starlark"
)

// FromJSON converts JSON to a Starlark value. Empty input is None.
// Integral numbers become ints, others floats.
func FromJSON(raw json.RawMessage) (starlark.Value, error) {
	if len(raw) == 0 {
		return starlark.None, nil
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return FromGo(v)
}

// FromGo converts a Go value to a Starlark value. Decoded-JSON shapes (nil,
// bool, string, json.Number, float64, []any, map[string]any) convert
// directly; anything else goes through encoding/json first. An integral
// float64 in int64's range [-2^63, 2^63) becomes an int; others stay floats.
func FromGo(v any) (starlark.Value, error) {
	switch t := v.(type) {
	case nil:
		return starlark.None, nil
	case bool:
		return starlark.Bool(t), nil
	case string:
		return starlark.String(t), nil
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return starlark.MakeInt64(i), nil
		}
		f, err := t.Float64()
		if err != nil {
			return nil, fmt.Errorf("number %q: %w", t, err)
		}
		return starlark.Float(f), nil
	case float64:
		// int64 holds [-2^63, 2^63): 2^63 itself (float64(math.MaxInt64)
		// rounds up to it) stays a float.
		if t == math.Trunc(t) && t >= -(1<<63) && t < 1<<63 {
			return starlark.MakeInt64(int64(t)), nil
		}
		return starlark.Float(t), nil
	case []any:
		elems := make([]starlark.Value, 0, len(t))
		for _, e := range t {
			sv, err := FromGo(e)
			if err != nil {
				return nil, err
			}
			elems = append(elems, sv)
		}
		return starlark.NewList(elems), nil
	case map[string]any:
		d := starlark.NewDict(len(t))
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			sv, err := FromGo(t[k])
			if err != nil {
				return nil, err
			}
			if err := d.SetKey(starlark.String(k), sv); err != nil {
				return nil, err
			}
		}
		return d, nil
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return nil, err
		}
		return FromJSON(b)
	}
}

// ToGo converts a Starlark value to a JSON-marshalable Go value: nil, bool,
// string, int64, float64, []any or map[string]any. Ints too large for int64
// become float64. Tuples and sets become slices.
func ToGo(v starlark.Value) (any, error) {
	switch t := v.(type) {
	case starlark.NoneType:
		return nil, nil
	case starlark.Bool:
		return bool(t), nil
	case starlark.String:
		return string(t), nil
	case starlark.Int:
		if i, ok := t.Int64(); ok {
			return i, nil
		}
		f, _ := starlark.AsFloat(v)
		return f, nil
	case starlark.Float:
		return float64(t), nil
	case *starlark.List:
		return iterToGo(t, t.Len())
	case starlark.Tuple:
		out := make([]any, 0, len(t))
		for _, e := range t {
			gv, err := ToGo(e)
			if err != nil {
				return nil, err
			}
			out = append(out, gv)
		}
		return out, nil
	case *starlark.Dict:
		out := make(map[string]any, t.Len())
		for _, pair := range t.Items() {
			ks, ok := pair[0].(starlark.String)
			if !ok {
				return nil, fmt.Errorf("dict key %s is not a string", pair[0])
			}
			gv, err := ToGo(pair[1])
			if err != nil {
				return nil, err
			}
			out[string(ks)] = gv
		}
		return out, nil
	case *starlark.Set:
		return iterToGo(t, t.Len())
	}
	return nil, fmt.Errorf("value of type %s is not JSON-convertible", v.Type())
}

func iterToGo(it starlark.Iterable, n int) (any, error) {
	out := make([]any, 0, n)
	iter := it.Iterate()
	defer iter.Done()
	var e starlark.Value
	for iter.Next(&e) {
		gv, err := ToGo(e)
		if err != nil {
			return nil, err
		}
		out = append(out, gv)
	}
	return out, nil
}

// ToJSON converts a Starlark value to JSON under ToGo's rules.
func ToJSON(v starlark.Value) (json.RawMessage, error) {
	gv, err := ToGo(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(gv)
}

// KwargsToJSON turns a built-in's arguments into one JSON object: keyword
// arguments become fields, optionally on top of a single positional dict.
// fn names the built-in in errors.
func KwargsToJSON(fn string, args starlark.Tuple, kwargs []starlark.Tuple) (json.RawMessage, error) {
	obj := map[string]any{}
	if len(args) == 1 {
		d, ok := args[0].(*starlark.Dict)
		if !ok {
			return nil, fmt.Errorf("%s: single positional argument must be a dict, got %s", fn, args[0].Type())
		}
		gv, err := ToGo(d)
		if err != nil {
			return nil, err
		}
		obj = gv.(map[string]any)
	} else if len(args) > 1 {
		return nil, fmt.Errorf("%s: pass arguments as keywords or one dict", fn)
	}
	for _, kv := range kwargs {
		k := string(kv[0].(starlark.String))
		gv, err := ToGo(kv[1])
		if err != nil {
			return nil, err
		}
		obj[k] = gv
	}
	return json.Marshal(obj)
}
