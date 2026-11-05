package modules_test

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/craggy-io/foundation-kit/bundle"
	"github.com/craggy-io/foundation-kit/kind"

	"github.com/craggy-io/script-services/modules"
)

// capture is a kind whose compiler keeps the contribution, so the test can
// read through FromContribution after Load.
var capture = kind.New[bundle.Contribution]("test.capture", 1)

func TestFromContribution(t *testing.T) {
	app := fstest.MapFS{
		"bundle.yaml":  {Data: []byte("apiVersion: craggy.io/bundle/v1\nmetadata: {id: test.app, version: 1.0.0}\ndependencies:\n  - id: test.lib\ncontributions:\n  - {id: main, kind: test.capture@1, path: main.star}\n")},
		"main.star":    {Data: []byte("M = 1\n")},
		"lib/own.star": {Data: []byte("OWN = 1\n")},
	}
	lib := fstest.MapFS{
		"bundle.yaml": {Data: []byte("apiVersion: craggy.io/bundle/v1\nmetadata: {id: test.lib, version: 1.0.0}\ncontributions: []\n")},
		"x.star":      {Data: []byte("X = 1\n")},
	}
	cat, err := bundle.Load(context.Background(), bundle.Options{
		Sources: []bundle.Source{bundle.Embedded("app", app), bundle.Embedded("lib", lib)},
		Compilers: []bundle.AnyCompiler{bundle.Compile(capture, func(_ context.Context, c bundle.Contribution) (bundle.Compiled[bundle.Contribution], []bundle.Diagnostic) {
			return bundle.Compiled[bundle.Contribution]{Value: c}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	def, _ := bundle.Get(cat, capture, bundle.Ref{Owner: "test.app", ID: "main"})
	src := modules.FromContribution(def.Value, map[string]string{"l": "test.lib", "gone": "test.gone"})
	ctx := context.Background()
	for p, want := range map[string]string{"lib/own.star": "OWN = 1\n", "@l/x.star": "X = 1\n", "@test.lib/x.star": "X = 1\n"} {
		if b, err := src.Read(ctx, p); err != nil || string(b) != want {
			t.Errorf("Read(%q) = %q, %v", p, b, err)
		}
	}
	if _, err := src.Read(ctx, "@l/missing.star"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing module err = %v", err)
	}
	for p, want := range map[string]string{"@other/x.star": "load alias @other", "@gone/x.star": "load alias @gone names dependency test.gone"} {
		if _, err := src.Read(ctx, p); err == nil || !strings.Contains(err.Error(), want) || errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Read(%q) err = %v, want one containing %q", p, err, want)
		}
	}
}
