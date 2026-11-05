package modules_test

import (
	"context"
	"errors"
	"fmt"

	"go.starlark.net/starlark"

	"github.com/craggy-io/script-services/dialect"
	"github.com/craggy-io/script-services/modules"
)

// Resolve maps load() specs to canonical paths: "./" and "../" relative to
// the loading file, "//" and bare paths relative to the module root.
func ExampleResolve() {
	for _, spec := range []string{"./util.star", "../top.star", "//lib/x.star", "lib/x.star", "../../out.star", "/abs.star"} {
		p, err := modules.Resolve("lib/text.star", spec)
		if err != nil {
			fmt.Println(spec, "->", err)
			continue
		}
		fmt.Println(spec, "->", p)
	}
	// Output:
	// ./util.star -> lib/util.star
	// ../top.star -> top.star
	// //lib/x.star -> lib/x.star
	// lib/x.star -> lib/x.star
	// ../../out.star -> module path "../../out.star" escapes the module root
	// /abs.star -> module path "/abs.star" is absolute: use //abs.star for a root path
}

// A Loader executes each module once and gives it a content address that
// ignores its path but covers its dependencies, each paired with the load()
// spec that named it.
func ExampleLoader_Module() {
	util := "def twice(s):\n    return s + s\n"
	text := "load('./util.star', 'twice')\ndef shout(s):\n    return twice(s.upper())\n"
	l := modules.NewLoader(modules.Map(map[string]string{
		"lib/util.star":       util,
		"lib/text.star":       text,
		"copy/deep/util.star": util,
		"copy/deep/text.star": text,
	}), modules.Options{MaxSteps: 100_000})
	ctx := context.Background()

	globals, ref, err := l.Module(ctx, "lib/text.star")
	if err != nil {
		fmt.Println(err)
		return
	}
	v, _ := starlark.Call(&starlark.Thread{}, globals["shout"], starlark.Tuple{starlark.String("ab")}, nil)
	fmt.Println(ref.Path, v)

	_, copyRef, _ := l.Module(ctx, "copy/deep/text.star")
	fmt.Println("same digest at another path:", copyRef.Digest == ref.Digest)

	_, utilRef, _ := l.Module(ctx, "lib/util.star")
	// The digest pairs each load() spec, as written, with what it loaded.
	deps := []modules.Dep{{Spec: "./util.star", Digest: utilRef.Digest}}
	fmt.Println("digest = Digest(source, direct deps):", ref.Digest == modules.Digest([]byte(text), deps))
	// Output:
	// lib/text.star "ABAB"
	// same digest at another path: true
	// digest = Digest(source, direct deps): true
}

// Bind installs load() on a host's own thread. Refs then lists every module
// the thread's programs loaded, transitively, sorted by path; Deps lists
// only the direct ones, by spec, which is what the program's digest covers.
func ExampleLoader_Bind() {
	l := modules.NewLoader(modules.Map(map[string]string{
		"lib/text.star": "load('./util.star', 'twice')\ndef shout(s):\n    return twice(s.upper())\n",
		"lib/util.star": "def twice(s):\n    return s + s\n",
	}), modules.Options{})
	thread := &starlark.Thread{Name: "main"}
	l.Bind(context.Background(), thread, "main.star")
	prog, err := dialect.Compile("main.star", []byte("load('./lib/text.star', 'shout')\nOUT = shout('hi')\n"), func(string) bool { return false })
	if err != nil {
		fmt.Println(err)
		return
	}
	globals, err := prog.Init(thread, nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(globals["OUT"])
	for _, r := range l.Refs(thread) {
		fmt.Println(r.Path)
	}
	for _, d := range l.Deps(thread) {
		fmt.Println("direct:", d.Spec)
	}
	// Output:
	// "HIHI"
	// lib/text.star
	// lib/util.star
	// direct: ./lib/text.star
}

// A load cycle fails with a *CycleError naming the chain; it is reachable
// with errors.As through the *Error of the module that hit it.
func ExampleCycleError() {
	l := modules.NewLoader(modules.Map(map[string]string{
		"a.star": "load('b.star', 'B')\nA = 1\n",
		"b.star": "load('a.star', 'A')\nB = 1\n",
	}), modules.Options{})
	_, _, err := l.Module(context.Background(), "a.star")
	var ce *modules.CycleError
	var me *modules.Error
	fmt.Println(errors.As(err, &ce), ce.Chain)
	fmt.Println(errors.As(err, &me), me.Path, me.Pos.Line)
	// Output:
	// true [a.star b.star a.star]
	// true a.star 1
}
