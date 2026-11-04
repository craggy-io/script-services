// Package schema lets scripts declare JSON Schemas, and converts what they
// declare into *jsonschema.Schema values for the host.
//
// The Starlark `schema` module (see Module):
//
//	schema.string(description = "", required = False)
//	schema.integer / schema.int, schema.number / schema.float,
//	schema.boolean / schema.bool, schema.any
//	schema.array(items = <schema>) / schema.list
//	schema.object({field: <schema>, ...})
//	schema.raw({...} or "{...}")   # a verbatim JSON Schema
//	schema.type("Name")            # a Go-syntax declaration (WithTypes only)
//
// Anywhere a host accepts a schema (see Of), a script may pass a schema.*
// value, a field dict ({"artifact": schema.string(required = True)}), a raw
// JSON Schema dict ({"type": "object", ...}), a JSON Schema string, or, with
// WithTypes, the name of a declared type. A dict holding any schema.* value
// is a field dict even when a field is named like a schema keyword.
package schema

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"github.com/google/jsonschema-go/jsonschema"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/craggy-io/script-services/types"
	"github.com/craggy-io/script-services/values"
)

// Option configures Module and Of.
type Option func(*config)

type config struct {
	types *types.Package
}

// WithTypes makes the declarations in pkg available: the module gains
// schema.type("Name"), and Of also accepts a declared type's name.
func WithTypes(pkg *types.Package) Option {
	return func(c *config) { c.types = pkg }
}

func newConfig(opts []Option) *config {
	c := &config{}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Value is a schema built by the schema module, plus the "required" flag it
// carries when it appears as an object field.
type Value struct {
	Schema   *jsonschema.Schema // the declared schema
	Required bool               // required = True was passed
}

var _ starlark.Value = (*Value)(nil)

// String renders the schema as schema(<json>).
func (s *Value) String() string {
	b, _ := json.Marshal(s.Schema)
	return "schema(" + string(b) + ")"
}

// Type returns "schema".
func (s *Value) Type() string { return "schema" }

// Freeze is a no-op: schema values are immutable to scripts.
func (s *Value) Freeze() {}

// Truth is always true.
func (s *Value) Truth() starlark.Bool { return starlark.True }

// Hash fails: schema values are unhashable.
func (s *Value) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable: schema") }

// Module returns the `schema` namespace for scripts.
func Module(opts ...Option) starlark.Value {
	c := newConfig(opts)
	mk := func(typ string) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
		return func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			var desc string
			var required bool
			if err := starlark.UnpackArgs(b.Name(), args, kwargs, "description?", &desc, "required?", &required); err != nil {
				return nil, err
			}
			return &Value{Schema: &jsonschema.Schema{Type: typ, Description: desc}, Required: required}, nil
		}
	}
	arrayFn := func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var items starlark.Value
		var desc string
		var required bool
		if err := starlark.UnpackArgs(b.Name(), args, kwargs, "items?", &items, "description?", &desc, "required?", &required); err != nil {
			return nil, err
		}
		s := &jsonschema.Schema{Type: "array", Description: desc}
		if items != nil {
			is, err := c.of(items)
			if err != nil {
				return nil, err
			}
			s.Items = is
		}
		return &Value{Schema: s, Required: required}, nil
	}
	objectFn := func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var fields *starlark.Dict
		var desc string
		var required bool
		if err := starlark.UnpackArgs(b.Name(), args, kwargs, "fields?", &fields, "description?", &desc, "required?", &required); err != nil {
			return nil, err
		}
		s, err := c.fieldsSchema(fields)
		if err != nil {
			return nil, err
		}
		s.Description = desc
		return &Value{Schema: s, Required: required}, nil
	}
	rawFn := func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		if len(args) != 1 || len(kwargs) != 0 {
			return nil, fmt.Errorf("schema.raw: pass one dict or JSON string")
		}
		s, err := rawSchema(args[0])
		if err != nil {
			return nil, err
		}
		return &Value{Schema: s}, nil
	}
	members := starlark.StringDict{
		"string":  starlark.NewBuiltin("schema.string", mk("string")),
		"integer": starlark.NewBuiltin("schema.integer", mk("integer")),
		"int":     starlark.NewBuiltin("schema.int", mk("integer")),
		"number":  starlark.NewBuiltin("schema.number", mk("number")),
		"float":   starlark.NewBuiltin("schema.float", mk("number")),
		"boolean": starlark.NewBuiltin("schema.boolean", mk("boolean")),
		"bool":    starlark.NewBuiltin("schema.bool", mk("boolean")),
		"any":     starlark.NewBuiltin("schema.any", mk("")),
		"array":   starlark.NewBuiltin("schema.array", arrayFn),
		"list":    starlark.NewBuiltin("schema.list", arrayFn),
		"object":  starlark.NewBuiltin("schema.object", objectFn),
		"raw":     starlark.NewBuiltin("schema.raw", rawFn),
	}
	if c.types != nil {
		members["type"] = starlark.NewBuiltin("schema.type", func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			var name, desc string
			var required bool
			if err := starlark.UnpackArgs(b.Name(), args, kwargs, "name", &name, "description?", &desc, "required?", &required); err != nil {
				return nil, err
			}
			s, err := c.types.Schema(name)
			if err != nil {
				return nil, fmt.Errorf("schema.type: %w", err)
			}
			if desc != "" {
				s.Description = desc
			}
			return &Value{Schema: s, Required: required}, nil
		})
	}
	return &starlarkstruct.Module{Name: "schema", Members: members}
}

// Of converts what a host accepts as a schema to a JSON Schema: a schema.*
// value, a field dict, a raw JSON Schema dict, a JSON Schema string, or None
// (nil schema). With WithTypes, a string naming a declared type is that
// type.
//
// A dict is a field dict when any of its values is a schema.* value (which
// includes schema.type values), whatever its keys: {"type":
// schema.string()} declares a field named "type". Otherwise a dict with a
// schema keyword key ("type", "properties", "description", ...) is raw
// JSON Schema, and any other dict is a field dict.
//
// The result is the caller's own: Of returns a deep copy, never a schema
// shared with the script's value.
func Of(v starlark.Value, opts ...Option) (*jsonschema.Schema, error) {
	s, err := newConfig(opts).of(v)
	if err != nil || s == nil {
		return s, err
	}
	return clone(s)
}

// clone deep-copies s through its JSON form, then restores PropertyOrder,
// the one field that form leaves out. (Schema.CloneSchemas shares
// Required, Enum and other non-schema fields with the original.)
func clone(s *jsonschema.Schema) (*jsonschema.Schema, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	var out jsonschema.Schema
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	copyOrder(&out, s)
	return &out, nil
}

var (
	schemaPtr   = reflect.TypeFor[*jsonschema.Schema]()
	schemaSlice = reflect.TypeFor[[]*jsonschema.Schema]()
	schemaMap   = reflect.TypeFor[map[string]*jsonschema.Schema]()
)

// copyOrder copies PropertyOrder from src into dst, which has the same
// shape, at every level.
func copyOrder(dst, src *jsonschema.Schema) {
	if dst == nil || src == nil {
		return
	}
	dst.PropertyOrder = append([]string(nil), src.PropertyOrder...)
	dv, sv := reflect.ValueOf(dst).Elem(), reflect.ValueOf(src).Elem()
	for i := range sv.NumField() {
		if !sv.Type().Field(i).IsExported() {
			continue
		}
		df, sf := dv.Field(i), sv.Field(i)
		switch sf.Type() {
		case schemaPtr:
			copyOrder(df.Interface().(*jsonschema.Schema), sf.Interface().(*jsonschema.Schema))
		case schemaSlice:
			d, s := df.Interface().([]*jsonschema.Schema), sf.Interface().([]*jsonschema.Schema)
			for j := range min(len(d), len(s)) {
				copyOrder(d[j], s[j])
			}
		case schemaMap:
			d, s := df.Interface().(map[string]*jsonschema.Schema), sf.Interface().(map[string]*jsonschema.Schema)
			for k, ss := range s {
				copyOrder(d[k], ss)
			}
		}
	}
}

// rawSchema converts a dict or JSON string to a schema verbatim, with no
// field-dict interpretation: schema.raw's whole contract.
func rawSchema(v starlark.Value) (*jsonschema.Schema, error) {
	switch t := v.(type) {
	case starlark.String:
		return Parse(json.RawMessage(string(t)))
	case *starlark.Dict:
		raw, err := values.ToJSON(t)
		if err != nil {
			return nil, err
		}
		return Parse(raw)
	}
	return nil, fmt.Errorf("schema.raw: pass one dict or JSON string, got %s", v.Type())
}

func (c *config) of(v starlark.Value) (*jsonschema.Schema, error) {
	return c.ofPath(v, 0, map[*starlark.Dict]bool{})
}

func (c *config) ofPath(v starlark.Value, depth int, seen map[*starlark.Dict]bool) (*jsonschema.Schema, error) {
	if depth > 10000 {
		return nil, fmt.Errorf("schema is nested too deeply")
	}
	switch t := v.(type) {
	case *Value:
		return t.Schema, nil
	case starlark.String:
		if c.types != nil && c.types.Has(string(t)) {
			return c.types.Schema(string(t))
		}
		return Parse(json.RawMessage(string(t)))
	case *starlark.Dict:
		if seen[t] {
			return nil, fmt.Errorf("cyclic schema dict")
		}
		seen[t] = true
		defer delete(seen, t)
		if looksLikeSchema(t) {
			raw, err := values.ToJSON(t)
			if err != nil {
				return nil, err
			}
			return Parse(raw)
		}
		return c.fieldsSchemaPath(t, depth, seen)
	case starlark.NoneType:
		return nil, nil
	}
	return nil, fmt.Errorf("expected a schema, dict or JSON string, got %s", v.Type())
}

// Parse parses a JSON Schema document and checks that it resolves (no bad
// $ref, pattern and so on). The boolean schemas "true" and "false" are
// accepted: true becomes an empty schema, false one that matches nothing.
func Parse(data json.RawMessage) (*jsonschema.Schema, error) {
	switch string(data) {
	case "true":
		return &jsonschema.Schema{}, nil
	case "false":
		return &jsonschema.Schema{Not: &jsonschema.Schema{}}, nil
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	if _, err := s.Resolve(nil); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	return &s, nil
}

var schemaKeywords = map[string]bool{
	"type": true, "properties": true, "items": true, "required": true,
	"$ref": true, "enum": true, "allOf": true, "anyOf": true, "oneOf": true,
	"additionalProperties": true, "description": true,
}

// looksLikeSchema reports whether d is raw JSON Schema rather than a field
// dict: it has a schema keyword key and no schema.* value. A schema.* value
// cannot appear in raw JSON Schema, so it marks a field dict even when a
// field is named like a keyword.
func looksLikeSchema(d *starlark.Dict) bool {
	keyword := false
	for _, pair := range d.Items() {
		if _, ok := pair[1].(*Value); ok {
			return false
		}
		if ks, ok := pair[0].(starlark.String); ok && schemaKeywords[string(ks)] {
			keyword = true
		}
	}
	return keyword
}

// fieldsSchema builds a closed object schema from a {name: schema} dict.
// Fields whose schema.* value says required = True are required.
func (c *config) fieldsSchema(d *starlark.Dict) (*jsonschema.Schema, error) {
	seen := map[*starlark.Dict]bool{}
	if d != nil {
		seen[d] = true
	}
	return c.fieldsSchemaPath(d, 0, seen)
}

func (c *config) fieldsSchemaPath(d *starlark.Dict, depth int, seen map[*starlark.Dict]bool) (*jsonschema.Schema, error) {
	s := &jsonschema.Schema{
		Type:       "object",
		Properties: map[string]*jsonschema.Schema{},
	}
	if d == nil {
		return s, nil // schema.object() with no fields stays open, as before
	}
	{
		var order []string
		for _, pair := range d.Items() {
			ks, ok := pair[0].(starlark.String)
			if !ok {
				return nil, fmt.Errorf("field name %s is not a string", pair[0])
			}
			name := string(ks)
			fs, err := c.ofPath(pair[1], depth+1, seen)
			if err != nil {
				return nil, fmt.Errorf("field %q: %w", name, err)
			}
			if fs == nil {
				fs = &jsonschema.Schema{}
			}
			s.Properties[name] = fs
			order = append(order, name)
			if sv, ok := pair[1].(*Value); ok && sv.Required {
				s.Required = append(s.Required, name)
			}
		}
		s.PropertyOrder = order
		sort.Strings(s.Required)
	}
	s.AdditionalProperties = &jsonschema.Schema{Not: &jsonschema.Schema{}}
	return s, nil
}
