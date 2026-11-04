package dialect_test

import (
	"context"
	"strings"
	"testing"

	"go.starlark.net/syntax"

	"github.com/craggy-io/script-services/check"
	"github.com/craggy-io/script-services/dialect"
	"github.com/craggy-io/script-services/modules"
	"github.com/craggy-io/script-services/script"
	"github.com/craggy-io/script-services/startest"
)

func TestFileOptions(t *testing.T) {
	want := syntax.FileOptions{Set: true, While: true, TopLevelControl: true, GlobalReassign: true}
	if *dialect.FileOptions != want {
		t.Fatalf("FileOptions = %+v, want %+v", *dialect.FileOptions, want)
	}
	if _, err := dialect.Parse("x.star", []byte("while True:\n    pass\n")); err != nil {
		t.Fatal(err)
	}
}

// Every entry point accepts and rejects exactly the same programs.
var programs = []struct {
	name string
	src  string
	ok   bool
}{
	{"while", "n = 0\nwhile n < 3:\n    n += 1\n", true},
	{"set", "s = set([1, 2, 2])\nN = len(s)\n", true},
	{"toplevel if/for", "x = 0\nif True:\n    for i in range(3):\n        x += i\n", true},
	{"global reassign", "x = 1\nx = 2\n", true},
	{"plain", "def f(a, *, b = 2):\n    return a + b\nY = f(1)\n", true},
	{"syntax error", "x = (\n", false},
	{"undefined", "x = nope\n", false},
	{"load in function", "def f():\n    load('a.star', 'x')\n", false},
}

func TestEveryEntryPointAcceptsTheSameDialect(t *testing.T) {
	ctx := context.Background()
	for _, p := range programs {
		t.Run(p.name, func(t *testing.T) {
			// check
			diags := check.Check(check.Env{}, "p.star", []byte(p.src))
			if (len(diags) == 0) != p.ok {
				t.Errorf("Check: %+v", diags)
			}
			// script.Exec
			_, err := script.Exec(ctx, script.Program{Filename: "p.star", Source: []byte(p.src)}, script.Options{Limits: script.DefaultLimits})
			if (err == nil) != p.ok {
				t.Errorf("Exec: %v", err)
			}
			// modules (load)
			l := modules.NewLoader(modules.Map(map[string]string{"p.star": p.src}), modules.Options{})
			_, _, err = l.Module(ctx, "p.star")
			if (err == nil) != p.ok {
				t.Errorf("modules: %v", err)
			}
			// startest: the file's top level is the program
			outs := startest.RunFile(ctx, modules.Map(map[string]string{"p_test.star": p.src + "\ndef test_x():\n    pass\n"}), "p_test.star", startest.Options{})
			passed := len(outs) == 1 && outs[0].Passed()
			if passed != p.ok {
				t.Errorf("startest: %+v", outs)
			}
		})
	}
}

func TestRecursionStaysOff(t *testing.T) {
	src := "def f(n):\n    return 0 if n == 0 else f(n - 1)\nX = f(2)\n"
	_, err := script.Exec(context.Background(), script.Program{Source: []byte(src)}, script.Options{})
	if err == nil || !strings.Contains(err.Error(), "called recursively") {
		t.Fatalf("Exec err = %v", err)
	}
	_, _, err = modules.NewLoader(modules.Map(map[string]string{"r.star": src}), modules.Options{}).Module(context.Background(), "r.star")
	if err == nil || !strings.Contains(err.Error(), "called recursively") {
		t.Fatalf("module err = %v", err)
	}
}
