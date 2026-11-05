# Types

[Project](../README.md) · [Documentation index](README.md) · [Schema](schema.md)

Available Go API: `github.com/craggy-io/script-services/types`.

## Purpose

Turn Go-syntax type declarations into JSON Schemas, so a script's inputs and
outputs can be declared the way Go programmers already write them, typically in a
`types.go` file next to the scripts.

## Features

- Parse a Go source file with `go/parser`; never compile or type-check it.
- Produce the schema shapes and required sets `jsonschema.For` (v0.4.3) derives
  from the same Go types, so a type moved between a Go handler and a declaration
  file keeps its schema, with a few [documented deviations](#parity-with-jsonschemafor).
- Closed objects in field order; only `omitempty`/`omitzero` make a field
  optional.
- Descriptions from `jsonschema` tags and comments; enums from an `enum:` line in
  a named type's doc comment.
- Positioned errors for recursive and unsupported types.

## Mental model

A declaration file is data, not code. `Parse` collects the file's type
declarations; `Schema(name)` expands one of them, inlining every declared type it
references, into a fresh `*jsonschema.Schema`. There are no `$defs` or `$ref`s,
which is why recursive types are rejected.

## Responsibilities and boundaries

`types` reads one file. It does not resolve other packages' types, run
`go/types`, or validate documents; validation belongs to jsonschema-go
(`Schema.Resolve(...).Validate`). Scripts reach these schemas through
[schema](schema.md) (`schema.WithTypes`), and the CLI prints them with
[`craggy-script types`](cli.md).

### Supported Go subset

| Go type | JSON Schema |
| --- | --- |
| `string` | `{"type":"string"}` |
| `bool` | `{"type":"boolean"}` |
| `int`, `int64` | `{"type":"integer"}` |
| `uint`, `uint64`, `uintptr` | `{"type":"integer","minimum":0}` |
| `int8`, `int16`, `int32`/`rune`, `uint8`/`byte`, `uint16`, `uint32` | integer with that kind's `minimum`/`maximum` |
| `float32`, `float64` | `{"type":"number"}` |
| `[]T` | `{"type":["null","array"],"items":T}` |
| `[N]T` (integer literal N) | `{"type":"array","items":T,"minItems":N,"maxItems":N}` |
| `[]byte` / `[]uint8` | `{"type":"string","contentEncoding":"base64","description":"base64-encoded bytes"}` |
| `map[string]T` (key of schema type string) | `{"type":"object","additionalProperties":T}` |
| `*T` | T's schema whose type also admits `null` (and whose `enum`, if any, also lists `null`) |
| `struct{...}` | closed object: `additionalProperties: false`, properties in field order |
| `any`, `interface{}`, `json.RawMessage` | unconstrained (`{}`, marshals as `true`) |
| `time.Time` | `{"type":"string","format":"date-time"}` |
| `time.Duration` | `{"type":"integer","description":"duration in nanoseconds"}` |
| another type declared in the file | inlined, named non-struct types included |

`time` and `encoding/json` must be imported by the file; import aliases are
honoured (`import t "time"` makes `t.Duration` work).

### Struct fields

- The property name is the `json` tag name, else the Go field name. `json:"-"`
  skips the field; unexported fields are skipped.
- A field is **required** unless its `json` tag has `omitempty` or `omitzero`,
  as in `jsonschema.For`. Pointers are required but nullable: `*int` without
  `omitempty` must be present and may be `null`. Slices and maps are required
  unless tagged, even though slices admit `null`.
- An embedded struct (or pointer to one) declared in the file, without a `json`
  name, promotes its fields. Fields sharing a JSON name follow encoding/json: the
  shallowest wins; among several promoted fields at that depth, the one with a
  `json` tag name wins, and with no tagged field or more than one, all of them
  are dropped silently (`TestEmbeddedSameDepthFollowsEncodingJSON`). Two of a
  struct's own fields sharing a JSON name are an error instead
  (`duplicate JSON field name "x"`), since that is a mistake in the
  declaration. Properties follow the winning fields' field order. An embedded
  field with a `json` name is an ordinary property.
- Field description: the `jsonschema:"..."` tag, else the field's doc comment,
  else its line comment. It replaces the referenced type's own description. A
  base64 field's description gets " (base64-encoded bytes)" appended unless it
  already mentions base64.
- A `jsonschema` tag must be non-empty and must not begin with `WORD=`.

### Named types, descriptions and enums

A declared type's description is its doc comment (for a grouped `type ( ... )`
declaration, the spec's own comment; a lone spec also uses the group's comment).
A line `enum: a, b, c` in that comment is removed from the description and
restricts the type to those values, parsed by the type's schema:

| Schema type | Enum values |
| --- | --- |
| string | as written; a quoted value (`"test"`) is unquoted |
| integer | decimal, `strconv.ParseInt` base 10 (`010` is ten; `0x10` is an error) |
| number | `strconv.ParseFloat` |
| boolean | `strconv.ParseBool` |
| anything else | error "enum needs a string, integer, number or bool type" |

### Parity with `jsonschema.For`

The shapes and required sets match `jsonschema.For` from jsonschema-go v0.4.3,
the deriver a Go handler registered with a capability registry goes through, so
one struct gives the same schema as a declaration file and as a Go handler.
`TestParityWithJSONSchemaFor` in [types_test.go](../types/types_test.go) declares
the same struct in Go and in a declaration file and requires byte-identical schema
JSON from `jsonschema.For` and from `Package.Schema`. The covered shapes are tags
and `jsonschema` descriptions, `omitempty`/`omitzero`, pointers with and without
`omitempty` (required and nullable without it), `int32`, `float64`, `[]bool`,
`[3]uint8`, maps, nested and pointer structs, `[]byte`, `json.RawMessage`,
`any`, `json:"-"`, unexported and untagged fields, and `map[string][]float32`.

Deliberate deviations, where `Package.Schema` differs from plain
`jsonschema.For`:

| Go type | `jsonschema.For` | `Package.Schema` |
| --- | --- | --- |
| `time.Time` | `{"type":"string"}` | adds `"format":"date-time"` |
| `time.Duration` | `{"type":"integer"}` | adds `"description":"duration in nanoseconds"` |
| `[]byte` | `{"type":["null","array"],"items":{"type":"integer","minimum":0,"maximum":255}}` | `{"type":"string","contentEncoding":"base64","description":"base64-encoded bytes"}`, which is what encoding/json produces |
| `json.RawMessage` | the `[]byte` array schema | unconstrained (`{}`), since encoding/json embeds it verbatim |
| promoted fields sharing a JSON name | decided by Go field names (`reflect.VisibleFields`) | decided by JSON names, as encoding/json does (see [struct fields](#struct-fields)) |
| an `enum:` line | not supported | `enum` from the doc comment |

The parity test passes `TypeSchemas` overrides for `json.RawMessage` and `[]byte`
and adds the base64 description to `For`'s side, so it pins everything else.

## Public interfaces

- `Parse(filename string, src []byte) (*Package, error)`
- `type Package struct { Name string; ... }`
- `(*Package).Names() []string` (sorted)
- `(*Package).Has(name string) bool`
- `(*Package).Schema(name string) (*jsonschema.Schema, error)`
- `type Error struct { Pos token.Position; Msg string }`
- `var Declarations = kind.New[*Package]("craggy.types", 1)`; `Compiler() bundle.AnyCompiler`;
  `const CodeInvalid bundle.Code = "types.invalid"`

### Declarations in bundles

A `types.go` file shipped in a bundle is a contribution of kind `craggy.types@1`
whose path names a `.go` file. `Compiler` parses it and derives every declared
type's schema, so a file that does not parse, or a type with no schema, is a
`types.invalid` load diagnostic with its line. The definition's value is the
`*Package`, ready for `schema.WithTypes`. The [CLI](cli.md) `types` command reads
it this way.

## Semantics, errors, and limits

- `Parse` requires a package clause and valid Go syntax; it returns the Go
  parser's error otherwise. Declarations other than types are ignored. Problems
  inside a type surface from `Schema`, not from `Parse`.
- `Schema` of an undeclared name fails with `types: unknown type "Name"`.
- Problems inside a declaration are `*types.Error` values formatted
  `file:line:col: message`:
  - `recursive type Node (Node -> Node)` (also through pointers, slices and
    embedding, for example `A -> B -> A`);
  - `unknown type Missing`, `unknown package time in time.Time (not imported)`;
  - `unsupported type chan int`, `func()`, `complex64`, `complex128`, `error`, and
    other packages' types such as `url.URL`;
  - `generic type T is unsupported`;
  - `unsupported map key type int: keys must be strings`;
  - `unsupported type interface{M()}: only empty interfaces are allowed`;
  - `array length must be an integer literal`;
  - `duplicate JSON field name "x"` (two of a struct's own fields),
    `empty jsonschema tag on field A`;
  - `type N: enum value "one" is not an integer` (positioned at the doc comment).
- Each `Schema` call returns a fresh schema the caller may modify.

## Examples

[`ExamplePackage_Schema`](../types/example_test.go) parses this file:

```go
package api

// DecompileIn is what apk.decompile takes.
type DecompileIn struct {
	Artifact string   `json:"artifact" jsonschema:"artifact id"`
	Depth    *int     `json:"depth,omitempty"` // how deep to go
	Level    Level    `json:"level"`
	Tags     []string `json:"tags,omitempty"`
}

// Level is a decompiler effort level.
// enum: fast, full
type Level string
```

and checks the schema `pkg.Schema("DecompileIn")` produces:

```json
{
  "type": "object",
  "properties": {
    "artifact": {"type": "string", "description": "artifact id"},
    "depth": {"type": ["null", "integer"], "description": "how deep to go"},
    "level": {"type": "string", "description": "Level is a decompiler effort level.", "enum": ["fast", "full"]},
    "tags": {"type": ["null", "array"], "items": {"type": "string"}}
  },
  "description": "DecompileIn is what apk.decompile takes.",
  "required": ["artifact", "level"],
  "additionalProperties": false
}
```

[`ExampleError`](../types/example_test.go) recovers a positioned `*types.Error`
with `errors.As`: `types.go:4:9: recursive type Node (Node -> Node)`.
