package startest_test

import (
	"context"
	"fmt"
	"testing/fstest"

	"github.com/craggy-io/script-services/script"
	"github.com/craggy-io/script-services/startest"
)

// Load reads a bundle whose bundle.yaml declares its test files. RunUnit runs
// one file's top level once, then each test_* function in name order on a
// fresh thread.
func ExampleRunUnit() {
	fsys := fstest.MapFS{
		"bundle.yaml": {Data: []byte(`apiVersion: craggy.io/bundle/v1
metadata: {id: example.text, version: 1.0.0}
contributions:
  - {id: text-test, kind: craggy.script-test@1, path: lib/text_test.star}
`)},
		"lib/text.star": {Data: []byte("def shout(s):\n    return s.upper() + '!'\n")},
		"lib/text_test.star": {Data: []byte(`load("./text.star", "shout")

def test_shout():
    assert.eq(shout("hi"), "HI!")

def test_wrong():
    assert.eq(shout("hi"), "hi", msg = "shouting")

def test_fails():
    msg = assert.fails(lambda: shout(1), "has no .upper field")
    print(msg)
`)},
	}
	opts := startest.Options{Limits: script.Limits{MaxSteps: 100_000}}
	defs, err := startest.Load(context.Background(), fsys, opts)
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, def := range defs {
		for _, o := range startest.RunUnit(context.Background(), def.Value, opts) {
			if o.Passed() {
				fmt.Println("PASS", o.Name(), o.Output)
				continue
			}
			fmt.Println("FAIL", o.Name(), o.Err)
		}
	}
	// Output:
	// PASS lib/text_test.star::test_fails [lib/text_test.star:11: int has no .upper field or method]
	// PASS lib/text_test.star::test_shout []
	// FAIL lib/text_test.star::test_wrong invalid: lib/text_test.star:7:14: assertion failed: shouting: "HI!" != "hi"
}
