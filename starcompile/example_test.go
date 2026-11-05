package starcompile_test

import (
	"context"
	"fmt"
	"testing/fstest"

	"go.starlark.net/starlark"

	"github.com/craggy-io/foundation-kit/bundle"
	"github.com/craggy-io/foundation-kit/kind"

	"github.com/craggy-io/script-services/check"
	"github.com/craggy-io/script-services/starcompile"
)

// Team is a domain's value: what a .star file declares with team().
type Team struct{ Members []string }

// TeamKind is declared next to its Go type, by the domain that owns it.
var TeamKind = kind.New[Team]("example.team", 1)

// TeamCompiler plugs the domain's built-in into the shared compiler: the
// file is checked with team predeclared, then its top level runs with a
// fresh collector, and the collector becomes the value.
func TeamCompiler() bundle.AnyCompiler {
	opts := starcompile.Options{Check: check.Env{Predeclared: []string{"team"}}}
	return bundle.Compile(TeamKind, starcompile.Compiler(opts, func(ctx context.Context, u *starcompile.Unit) (bundle.Compiled[Team], []bundle.Diagnostic) {
		var t Team
		team := starlark.NewBuiltin("team", func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
			for _, a := range args {
				s, _ := starlark.AsString(a)
				t.Members = append(t.Members, s)
			}
			return starlark.None, nil
		})
		if _, err := u.Exec(ctx, starcompile.ExecOptions{Predeclared: starlark.StringDict{"team": team}}); err != nil {
			return bundle.Compiled[Team]{}, u.Diagnostics(err)
		}
		return bundle.Compiled[Team]{Value: t}, nil
	}))
}

// A domain kind compiled from a bundle. The shared library is another
// bundle, declared as a dependency and loaded as @<dependency-id>/path.
func ExampleCompiler() {
	app := fstest.MapFS{
		"bundle.yaml": {Data: []byte(`apiVersion: craggy.io/bundle/v1
metadata: {id: example.app, version: 1.0.0}
dependencies:
  - id: example.names
contributions:
  - {id: reviewers, kind: example.team@1, path: teams/reviewers.star}
`)},
		"teams/reviewers.star": {Data: []byte("load('@example.names/names.star', 'ADA', 'GRACE')\nteam(ADA, GRACE)\n")},
	}
	names := fstest.MapFS{
		"bundle.yaml": {Data: []byte("apiVersion: craggy.io/bundle/v1\nmetadata: {id: example.names, version: 1.0.0}\ncontributions: []\n")},
		"names.star":  {Data: []byte("ADA = 'ada'\nGRACE = 'grace'\n")},
	}
	cat, err := bundle.Load(context.Background(), bundle.Options{
		Sources:   []bundle.Source{bundle.Embedded("app", app), bundle.Embedded("names", names)},
		Compilers: []bundle.AnyCompiler{TeamCompiler()},
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, def := range bundle.All(cat, TeamKind) {
		fmt.Println(def.Ref, def.Value.Members)
	}
	// Output:
	// example.app#reviewers [ada grace]
}

// The generic script kind keeps the checked Unit, to run later.
func ExampleScripts() {
	app := fstest.MapFS{
		"bundle.yaml": {Data: []byte("apiVersion: craggy.io/bundle/v1\nmetadata: {id: example.app, version: 1.0.0}\ncontributions:\n  - {id: hello, kind: craggy.script@1, path: hello.star}\n")},
		"hello.star":  {Data: []byte("GREETING = 'hello'\n")},
	}
	cat, err := bundle.Load(context.Background(), bundle.Options{
		Sources:   []bundle.Source{bundle.Embedded("app", app)},
		Compilers: []bundle.AnyCompiler{starcompile.Scripts(starcompile.Options{})},
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	def, _ := bundle.Get(cat, starcompile.Script, bundle.Ref{Owner: "example.app", ID: "hello"})
	ex, err := def.Value.Exec(context.Background(), starcompile.ExecOptions{})
	fmt.Println(def.Value.Path(), ex.Globals["GREETING"], err)
	// Output:
	// hello.star "hello" <nil>
}
