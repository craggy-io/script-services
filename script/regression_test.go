package script_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.starlark.net/starlark"

	"github.com/craggy-io/script-services/dialect"
	"github.com/craggy-io/script-services/modules"
	"github.com/craggy-io/script-services/script"
)

// A cyclic entry result used to overflow the Go stack fatally in result
// conversion; it must now be an ordinary *script.Error.
func TestExecCyclicResult(t *testing.T) {
	_, err := script.Exec(context.Background(), script.Program{
		Source: []byte("def run():\n    l = []\n    l.append(l)\n    return l\n"),
	}, script.Options{Limits: lim})
	var se *script.Error
	if !errors.As(err, &se) || se.Code != script.Invalid || !strings.Contains(se.Message, "cyclic") {
		t.Fatalf("cyclic result err = %v", err)
	}
}

var errQuota = errors.New("quota exhausted")

func slowBuiltin(delay time.Duration, err error) *starlark.Builtin {
	return starlark.NewBuiltin("slow", func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
		time.Sleep(delay)
		if err != nil {
			return nil, err
		}
		return starlark.String("ok"), nil
	})
}

// A genuine host error that returns after Limits.Timeout (built-ins are
// not interrupted) keeps its own message; it is not a timeout.
func TestExecLateHostErrorIsNotTimeout(t *testing.T) {
	_, err := script.Exec(context.Background(), script.Program{
		Source:      []byte("def run():\n    return slow()\n"),
		Predeclared: starlark.StringDict{"slow": slowBuiltin(150*time.Millisecond, errQuota)},
	}, script.Options{Limits: script.Limits{MaxSteps: lim.MaxSteps, Timeout: 50 * time.Millisecond}})
	var se *script.Error
	if !errors.As(err, &se) {
		t.Fatalf("err = %v", err)
	}
	if se.TimedOut || !strings.Contains(se.Message, "quota exhausted") || !errors.Is(err, errQuota) {
		t.Fatalf("late host error = %+v (err %v)", se, err)
	}
}

// Call must not sniff the words "time limit exceeded" out of arbitrary
// error text, and its genuine timeout stays TimedOut.
func TestCallTimeoutDetectionIsNotStringSniffing(t *testing.T) {
	th := script.NewThread("defs", script.Limits{}, nil)
	g, err := starlark.ExecFileOptions(dialect.FileOptions, th, "defs.star",
		"def phrase():\n    return slow()\ndef spin():\n    while True:\n        pass\n",
		starlark.StringDict{"slow": slowBuiltin(0, errors.New("upstream said time limit exceeded by user"))})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = script.Call(context.Background(), script.Limits{Timeout: 5 * time.Second}, "phrase", g["phrase"].(starlark.Callable), nil, nil)
	var se *script.Error
	if !errors.As(err, &se) || se.TimedOut || !strings.Contains(se.Message, "upstream said") {
		t.Fatalf("phrase err = %v", err)
	}
	_, _, err = script.Call(context.Background(), script.Limits{Timeout: 30 * time.Millisecond}, "spin", g["spin"].(starlark.Callable), nil, nil)
	if !errors.As(err, &se) || !se.TimedOut {
		t.Fatalf("spin err = %v", err)
	}
}

// Sentinel reachability promised by AGENTS.md: ErrNoSource and CycleError
// through a *script.Error from Exec.
func TestExecSentinelsThroughScriptError(t *testing.T) {
	_, err := script.Exec(context.Background(), script.Program{
		Source: []byte("load('./x.star', 'x')\n"),
	}, script.Options{Limits: lim})
	if !errors.Is(err, modules.ErrNoSource) {
		t.Fatalf("load without modules err = %v, want ErrNoSource", err)
	}

	loader := modules.NewLoader(modules.Map(map[string]string{
		"a.star": "load('./b.star', 'x')\nx = 1\n",
		"b.star": "load('./a.star', 'x')\nx = 1\n",
	}), modules.Options{MaxSteps: lim.MaxSteps})
	_, err = script.Exec(context.Background(), script.Program{
		Filename: "main.star",
		Source:   []byte("load('./a.star', 'x')\n"),
		Modules:  loader,
	}, script.Options{Limits: lim})
	var ce *modules.CycleError
	if !errors.As(err, &ce) {
		t.Fatalf("cycle err = %v, want *modules.CycleError", err)
	}
	if len(ce.Chain) < 2 || ce.Chain[0] != ce.Chain[len(ce.Chain)-1] {
		t.Fatalf("cycle chain = %v", ce.Chain)
	}
}
