# Documentation

[Project](../README.md) · [Agent guide](../AGENTS.md)

Every package has a Go implementation and tests. Each guide describes the
available API, its semantics and limits, and links to the package's
`example_test.go`, whose `// Output:` checks `go test` verifies. Operations that
run code or read modules take a context first; `check` and the pure conversion
packages do not.

## Package guides

Foundations:

- [Dialect](dialect.md): the one Starlark dialect every entry point parses.
- [Values](values.md): Starlark ↔ Go ↔ JSON conversion rules.
- [Types](types.md): Go-syntax declarations to JSON Schema.

Loading, declaring and testing:

- [Modules](modules.md): `load()` resolution, sources (`DirRoot` for directories), caching, cycles and content digests.
- [Schema](schema.md): the Starlark `schema` module and `schema.Of`.
- [Startest](startest.md): `*_test.star` files and the `assert` module.

Hosts:

- [Script](script.md): Exec, limits, classified errors and digests.
- [Check](check.md): static checking against an Env of callables.
- [CLI](cli.md): the `craggy-script` command.

Each package guide uses the same sections: purpose, features, mental model,
responsibilities and boundaries, public interfaces, semantics/errors/limits, and
examples.

## Branding

The two SVG wordmarks in `assets/` are unchanged copies of the Craggy brand kit's
light/dark variants, as used by artifact-services. The root README selects the
appropriate variant by display theme. `assets/script-services-architecture.svg`
follows the artifact-services architecture diagram's palette and type.
