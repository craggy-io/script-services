package memlimit_test

import (
	"strings"
	"testing"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/craggy-io/script-services/memlimit"
)

// run executes src on a thread limited to max bytes and maxSteps steps.
func run(t *testing.T, src string, max int64, maxSteps uint64) error {
	t.Helper()
	thread := &starlark.Thread{Name: "test"}
	memlimit.Install(thread, max, maxSteps)
	_, err := starlark.ExecFileOptions(&syntax.FileOptions{While: true, TopLevelControl: true, GlobalReassign: true}, thread, "test.star", src, nil)
	return err
}

func exceeded(err error) bool {
	return err != nil && strings.Contains(err.Error(), "Starlark computation cancelled: "+memlimit.Reason+" (held about")
}

const limit = 32 << 20

// Every way a thread can keep values is seen: a global, a local, a
// comprehension's accumulator (only on the operand stack), a dict, a
// closure's captured variable, a bound method's receiver, a string grown by
// concatenation.
func TestAThreadThatHoldsTooMuchStops(t *testing.T) {
	for name, src := range map[string]string{
		"global list": "big = []\nfor i in range(100000):\n    big.append('x' * 10000 + str(i))\n",
		"local list": `
def f():
    big = []
    for i in range(100000):
        big.append('x' * 10000 + str(i))
f()
`,
		"comprehension": "def f():\n    return len(['x' * 10000 + str(i) for i in range(100000)])\nf()\n",
		"dict":          "d = {i: 'x' * 10000 + str(i) for i in range(100000)}\n",
		"closure": `
def f():
    acc = []
    def add(x):
        acc.append(x)
    for i in range(100000):
        add('x' * 10000 + str(i))
f()
`,
		"bound method": `
def f():
    add = [].append
    for i in range(100000):
        add('x' * 10000 + str(i))
f()
`,
		"string growth": "s = ''\nfor i in range(100000):\n    s += 'x' * 10000\n",
		"nested":        "rows = [{'k': [str(i) * 2000 for j in range(4)]} for i in range(100000)]\n",
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(t, src, limit, 0); !exceeded(err) {
				t.Fatalf("err = %v, want the memory limit", err)
			}
		})
	}
}

// What a thread drops isn't held: allocating far more than the limit in
// garbage, or holding the same big value in many places, runs to the end.
func TestGarbageAndSharingDontCount(t *testing.T) {
	for name, src := range map[string]string{
		"garbage": "n = 0\nfor i in range(20000):\n    s = 'x' * 100000 + str(i)\n    n += len(s)\n",
		"shared":  "s = 'x' * 1000000\nmany = [s for i in range(10000)]\n",
		"small":   "rows = [{'id': i, 'name': 'row %d' % i} for i in range(10000)]\n",
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(t, src, limit, 0); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Install still keeps the step limit, with the interpreter's own message,
// and with no memory bound it is just SetMaxExecutionSteps.
func TestStepsStillBound(t *testing.T) {
	loop := "n = 0\nfor i in range(1000000):\n    n += 1\n"
	for _, max := range []int64{0, limit} {
		err := run(t, loop, max, 10_000)
		if err == nil || !strings.Contains(err.Error(), "Starlark computation cancelled: too many steps") {
			t.Fatalf("max %d: err = %v", max, err)
		}
		if err := run(t, loop, max, 0); err != nil {
			t.Fatalf("max %d unbounded steps: %v", max, err)
		}
	}
}

// Held reads each frame's operand stack (go.starlark.net's frame layout):
// a comprehension's accumulator is seen while it is built.
func TestHeldSeesAComprehension(t *testing.T) {
	var held int64
	probe := starlark.NewBuiltin("probe", func(thread *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
		held = max(held, memlimit.Held(thread))
		return starlark.None, nil
	})
	src := "def f():\n    return len([probe() or ('x' * 1000 + str(i)) for i in range(2000)])\nf()\n"
	if _, err := starlark.ExecFileOptions(&syntax.FileOptions{}, &starlark.Thread{}, "t.star", src, starlark.StringDict{"probe": probe}); err != nil {
		t.Fatal(err)
	}
	if held < 1500*1000 {
		t.Fatalf("held %d while building ~2 MB, want the accumulator counted", held)
	}
}

func BenchmarkCheckpoint(b *testing.B) {
	src := "n = 0\nfor i in range(1000000):\n    n += i\n"
	for _, c := range []struct {
		name string
		max  int64
	}{{"unlimited", 0}, {"limited", 64 << 20}} {
		b.Run(c.name, func(b *testing.B) {
			for b.Loop() {
				thread := &starlark.Thread{}
				memlimit.Install(thread, c.max, 0)
				if _, err := starlark.ExecFileOptions(&syntax.FileOptions{TopLevelControl: true, GlobalReassign: true}, thread, "b.star", src, nil); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
