package types_test

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"

	"github.com/craggy-io/foundation-kit/bundle"

	"github.com/craggy-io/script-services/types"
)

func TestDeclarationsKind(t *testing.T) {
	load := func(src string) (*bundle.Catalog, error) {
		fsys := fstest.MapFS{
			"bundle.yaml":  {Data: []byte("apiVersion: craggy.io/bundle/v1\nmetadata: {id: test.types, version: 1.0.0}\ncontributions:\n  - {id: api, kind: craggy.types@1, path: api/types.go}\n")},
			"api/types.go": {Data: []byte(src)},
		}
		return bundle.Load(context.Background(), bundle.Options{Sources: []bundle.Source{bundle.Embedded("t", fsys)}, Compilers: []bundle.AnyCompiler{types.Compiler()}})
	}
	cat, err := load("package api\n\ntype In struct {\n\tWho string `json:\"who\"`\n}\n")
	if err != nil {
		t.Fatal(err)
	}
	def, ok := bundle.Get(cat, types.Declarations, bundle.Ref{Owner: "test.types", ID: "api"})
	if !ok || !def.Value.Has("In") {
		t.Fatalf("definition = %+v, %v", def, ok)
	}
	for src, line := range map[string]int{
		"package api\n\ntype In struct {\n":                  3,
		"package api\n\ntype In struct {\n\tC chan int\n}\n": 4,
	} {
		_, err := load(src)
		var le *bundle.LoadError
		if !errors.As(err, &le) || len(le.Diagnostics) != 1 || le.Diagnostics[0].Code != types.CodeInvalid || le.Diagnostics[0].Path != "api/types.go" || le.Diagnostics[0].Line != line {
			t.Errorf("%q: err = %v %+v", src, err, le.Diagnostics)
		}
	}
}
