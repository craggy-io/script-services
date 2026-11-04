package script_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.starlark.net/starlark"

	"github.com/craggy-io/script-services/modules"
	"github.com/craggy-io/script-services/script"
)

// Exec runs the module body, then run(input). load() goes through a
// modules.Loader; the result carries every module used and a digest over the
// source and its direct dependencies' digests, the same digest the file
// would have as a module.
func ExampleExec() {
	loader := modules.NewLoader(modules.Map(map[string]string{
		"lib/text.star": "load('./util.star', 'twice')\ndef shout(s):\n    return twice(s.upper())\n",
		"lib/util.star": "def twice(s):\n    return s + s\n",
	}), modules.Options{MaxSteps: script.DefaultLimits.MaxSteps})

	src := []byte(`
load("./lib/text.star", "shout")

print("starting")

def run(input):
    return {"greeting": shout(input["who"])}
`)
	res, err := script.Exec(context.Background(), script.Program{
		Filename: "main.star",
		Source:   src,
		Input:    map[string]any{"who": "ab"},
		Modules:  loader,
	}, script.Options{
		Limits: script.DefaultLimits,
		Print: func(pos script.Position, msg string) {
			fmt.Printf("%s:%d: %s\n", pos.File, pos.Line, msg)
		},
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(string(res.Value))
	for _, m := range res.Modules {
		fmt.Println(m.Path)
	}
	// The digest covers the source and its direct load()s; lib/util.star
	// counts through lib/text.star's digest.
	fmt.Println(res.Digest == modules.Digest(src, []modules.Dep{{Spec: "./lib/text.star", Digest: res.Modules[0].Digest}}))
	// Output:
	// main.star:4: starting
	// {"greeting":"ABAB"}
	// lib/text.star
	// lib/util.star
	// true
}

// quotaError is a host built-in's own error type.
type quotaError struct{ left int }

func (e *quotaError) Error() string { return fmt.Sprintf("quota exhausted (%d left)", e.left) }

// A failure is a classified *script.Error positioned in the script; its
// Unwrap reaches the error a host built-in returned.
func ExampleError() {
	host := starlark.StringDict{
		"fetch": starlark.NewBuiltin("fetch", func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
			return nil, &quotaError{left: 0}
		}),
	}
	src := "def helper():\n    return fetch()\n\ndef run():\n    return helper()\n"
	_, err := script.Exec(context.Background(), script.Program{Filename: "job.star", Source: []byte(src), Predeclared: host},
		script.Options{Limits: script.Limits{MaxSteps: 10_000, Timeout: time.Second}})

	var se *script.Error
	if errors.As(err, &se) {
		fmt.Println(se.Code, se.Pos.Line, se.Message)
		for _, f := range se.Backtrace {
			fmt.Printf("  %s %s:%d\n", f.Func, f.Pos.File, f.Pos.Line)
		}
	}
	var qe *quotaError
	fmt.Println("host error reachable:", errors.As(err, &qe))
	fmt.Println(err)
	// Output:
	// invalid 2 quota exhausted (0 left)
	//   run job.star:5
	//   helper job.star:2
	//   fetch <builtin>:0
	// host error reachable: true
	// invalid: job.star:2:17: quota exhausted (0 left)
}

// Limits.MaxSteps stops runaway loops as Invalid; Limits.Timeout sets
// TimedOut; a cancelled context is Cancelled.
func ExampleLimits() {
	spin := script.Program{Filename: "spin.star", Source: []byte("while True:\n    pass\n")}

	_, err := script.Exec(context.Background(), spin, script.Options{Limits: script.Limits{MaxSteps: 1000}})
	var se *script.Error
	errors.As(err, &se)
	fmt.Println(se.Code, se.TimedOut, se.Message)

	_, err = script.Exec(context.Background(), spin, script.Options{Limits: script.Limits{Timeout: 20 * time.Millisecond}})
	errors.As(err, &se)
	fmt.Println(se.Code, se.TimedOut, se.Message)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = script.Exec(ctx, spin, script.Options{})
	errors.As(err, &se)
	fmt.Println(se.Code)
	// Output:
	// invalid false Starlark computation cancelled: too many steps
	// invalid true script exceeded its time limit
	// cancelled
}
