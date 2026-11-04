# Schema

[Project](../README.md) · [Documentation index](README.md) · [Types](types.md)

Available Go API: `github.com/craggy-io/script-services/schema`.

## Purpose

Let scripts declare JSON Schemas, and convert what they declare into
`*jsonschema.Schema` values for the host.

## Features

- A Starlark `schema` module: `schema.string()`, `schema.int()`,
  `schema.list(items = ...)`, `schema.object({...})`, `schema.raw(...)` and more.
- `schema.Of` accepts every form a script might pass: schema values, field dicts,
  raw JSON Schema dicts, JSON strings, `None`, and declared type names.
- `WithTypes` connects a [types](types.md) declaration file:
  `schema.type("Name")` in scripts and type names in `Of`.
- `Parse` reads a JSON Schema document and checks that it resolves.

## Mental model

A host decides where it accepts a schema (a tool's `INPUT` global, an argument to
one of its built-ins) and calls `schema.Of` on the value it finds. The script
chooses the most convenient spelling; `Of` normalises all of them to one
`*jsonschema.Schema`. Field dicts are the short form: each key is a property, each
value a schema, and `required = True` on a schema value marks the property
required.

## Responsibilities and boundaries

`schema` builds and parses schemas; validating documents against them is
jsonschema-go's job (`s.Resolve(nil)` then `Validate`). It does not decide where
scripts declare schemas or what a host does with them.

### The `schema` module

| Member | Arguments | Schema |
| --- | --- | --- |
| `string` | `description = "", required = False` | `{"type":"string"}` |
| `integer`, `int` | same | `{"type":"integer"}` |
| `number`, `float` | same | `{"type":"number"}` |
| `boolean`, `bool` | same | `{"type":"boolean"}` |
| `any` | same | `{}` (unconstrained) |
| `array`, `list` | `items = <schema>, description = "", required = False` | `{"type":"array","items":...}`; `items` takes any form `Of` accepts |
| `object` | `fields = {...}, description = "", required = False` | closed object from a field dict; with no fields, an open object schema (no `additionalProperties`) |
| `raw` | one positional dict or JSON string, no keywords | that JSON Schema verbatim (after `Parse`) |
| `type` | `name, description = "", required = False` | the declared type's schema; only with `WithTypes` |

Each returns a `*schema.Value`: a Starlark value of type `"schema"` that prints as
`schema(<json>)`, is always truthy and is unhashable. `description` on
`schema.type` replaces the type's own description.

### What `Of` accepts

| Value | Result |
| --- | --- |
| `*schema.Value` | (a copy of) its `Schema` |
| string naming a declared type (with `WithTypes`) | that type's schema |
| other string | parsed as a JSON Schema document (`Parse`) |
| dict with any `schema.*` value among its values (`schema.type` values included) | field dict (below), whatever its keys: `{"type": schema.string(), "name": schema.string()}` declares fields named `type` and `name` |
| other dict with any schema keyword key (`type`, `properties`, `items`, `required`, `$ref`, `enum`, `allOf`, `anyOf`, `oneOf`, `additionalProperties`, `description`) | converted with `values.ToJSON` and parsed as a raw JSON Schema |
| any other dict | field dict: closed object (`additionalProperties: false`), properties in insertion order, `required` (sorted) from `required = True` schema values; a `None` field value is unconstrained |
| `None` | nil schema, nil error |

A `schema.*` value can never appear inside raw JSON Schema (it is not
JSON-convertible), so its presence settles which form a dict is.

## Public interfaces

- `Module(opts ...Option) starlark.Value`
- `Of(v starlark.Value, opts ...Option) (*jsonschema.Schema, error)`
- `Parse(data json.RawMessage) (*jsonschema.Schema, error)`
- `WithTypes(pkg *types.Package) Option`
- `type Value struct { Schema *jsonschema.Schema; Required bool }` (implements `starlark.Value`)

## Semantics, errors, and limits

- `Parse` accepts the boolean schemas `true` (empty schema) and `false` (a schema
  matching nothing, `{"not":{}}`). Other documents must unmarshal and
  `Resolve(nil)`; bad JSON, an unresolvable `$ref` or a bad pattern is an error
  prefixed `schema:`.
- `Of` of an unsupported value fails with
  "expected a schema, dict or JSON string, got T". A non-string field name fails
  with "field name 1 is not a string"; a bad field schema with `field "x": ...`.
- `schema.raw` with anything but one positional argument fails with
  "schema.raw: pass one dict or JSON string". `schema.type` of an unknown name fails
  with `schema.type: types: unknown type "Nope"`. Without `WithTypes`, the module
  has no `type` member ("schema has no .type field or method"), and `Of` treats a
  type name as JSON, which fails.
- `required = True` only has an effect on schema values used as field-dict values.
  A raw schema dict as a field value cannot be marked required that way.
- A field dict may name fields like schema keywords (`"type"`, `"description"`,
  ...) as long as at least one value is a `schema.*` value
  (`TestFieldNamedLikeKeyword`). A dict whose values are all plain data or `None`
  and that has a keyword key is still raw JSON Schema; `schema.object({...})`
  always treats its argument as a field dict.
- `Of` returns a deep copy the caller owns: mutating it (properties, `required`,
  nested schemas) does not change the script's value or what a later `Of`
  returns (`TestOfReturnsACopy`). Property order is kept.

## Examples

[`ExampleOf`](../schema/example_test.go) runs a script with the module
predeclared and converts its field dict:

```python
INPUT = {
    "artifact": schema.string(required = True, description = "artifact id"),
    "depth": schema.int(),
    "tags": schema.list(items = schema.string()),
}
```

```go
predeclared := starlark.StringDict{"schema": schema.Module()}
prog, _ := dialect.Compile("tool.star", src, predeclared.Has)
globals, _ := prog.Init(&starlark.Thread{}, predeclared)
s, err := schema.Of(globals["INPUT"])
// {"type":"object","properties":{"artifact":{...},"depth":{...},"tags":{...}},
//  "required":["artifact"],"additionalProperties":false}
```

[`ExampleWithTypes`](../schema/example_test.go) passes the same
`schema.WithTypes(pkg)` option to `Module` and `Of`, so a script can write
`{"level": schema.type("Level", required = True)}` and the host can accept the
bare string `"Level"`. [`ExampleParse`](../schema/example_test.go) shows the
boolean schemas and an unresolvable `$ref` being rejected.
