package dialect_test

import (
	"fmt"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/craggy-io/script-services/dialect"
)

// A notebook-style program: top-level loops, while, set() and global
// reassignment all compile in the Craggy dialect.
func ExampleCompile() {
	src := []byte(`
seen = set()
for w in ["a", "b", "a"]:
    seen.add(w)
n = 0
while n < len(seen):
    n += 1
n = n * 10
`)
	prog, err := dialect.Compile("notebook.star", src, func(string) bool { return false })
	if err != nil {
		fmt.Println(err)
		return
	}
	globals, err := prog.Init(&starlark.Thread{}, nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(globals["n"])
	// Output: 20
}

// Syntax errors are syntax.Error values carrying their position.
func ExampleParse() {
	_, err := dialect.Parse("broken.star", []byte("x = (\n"))
	if se, ok := err.(syntax.Error); ok {
		fmt.Printf("%d:%d %s\n", se.Pos.Line, se.Pos.Col, se.Msg)
	}
	// Output: 2:1 got end of file, want primary expression
}
