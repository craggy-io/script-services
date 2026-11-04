package startest_test

import (
	"context"
	"fmt"

	"github.com/craggy-io/script-services/modules"
	"github.com/craggy-io/script-services/script"
	"github.com/craggy-io/script-services/startest"
)

// RunFile runs a *_test.star file's top level once, then each test_*
// function in name order on a fresh thread.
func ExampleRunFile() {
	src := modules.Map(map[string]string{
		"lib/text.star": "def shout(s):\n    return s.upper() + '!'\n",
		"lib/text_test.star": `load("./text.star", "shout")

def test_shout():
    assert.eq(shout("hi"), "HI!")

def test_wrong():
    assert.eq(shout("hi"), "hi", msg = "shouting")

def test_fails():
    msg = assert.fails(lambda: shout(1), "has no .upper field")
    print(msg)
`,
	})
	outs := startest.RunFile(context.Background(), src, "lib/text_test.star", startest.Options{
		Limits: script.Limits{MaxSteps: 100_000},
	})
	for _, o := range outs {
		if o.Passed() {
			fmt.Println("PASS", o.Name(), o.Output)
			continue
		}
		fmt.Println("FAIL", o.Name(), o.Err)
	}
	// Output:
	// PASS lib/text_test.star::test_fails [lib/text_test.star:11: int has no .upper field or method]
	// PASS lib/text_test.star::test_shout []
	// FAIL lib/text_test.star::test_wrong invalid: lib/text_test.star:7:14: assertion failed: shouting: "HI!" != "hi"
}
