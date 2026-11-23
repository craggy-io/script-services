package script_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/craggy-io/script-services/modules"
	"github.com/craggy-io/script-services/script"
)

const hog = `
def run(input):
    rows = []
    for i in range(100000):
        rows.append("x" * 10000 + str(i))
    return len(rows)
`

// A program over Limits.MaxMemory stops with MemoryExceeded and a message
// saying what it held; under the limit, or with no limit, it runs as before.
func TestMaxMemoryStopsAProgramThatHoldsTooMuch(t *testing.T) {
	ctx := context.Background()
	_, err := script.Exec(ctx, script.Program{Source: []byte(hog)}, script.Options{Limits: script.Limits{MaxSteps: 5_000_000, MaxMemory: 32 << 20}})
	var se *script.Error
	if !errors.As(err, &se) || se.Code != script.Invalid || !se.MemoryExceeded || se.TimedOut ||
		!strings.HasPrefix(se.Message, script.MemoryLimitMessage+" (held about ") || !strings.Contains(se.Message, "limit 32 MiB)") {
		t.Fatalf("err = %#v", err)
	}
	if se.Pos.Line == 0 {
		t.Fatalf("no position: %+v", se)
	}
	if raw, _ := json.Marshal(se); !strings.Contains(string(raw), `"memoryExceeded":true`) {
		t.Fatalf("json %s", raw)
	}

	small := strings.Replace(hog, "100000", "100", 1)
	for _, max := range []int64{0, 32 << 20} {
		res, err := script.Exec(ctx, script.Program{Source: []byte(small)}, script.Options{Limits: script.Limits{MaxSteps: 5_000_000, MaxMemory: max}})
		if err != nil || string(res.Value) != "100" {
			t.Fatalf("max %d: %s, %v", max, res.Value, err)
		}
	}
}

// A module's top level is bounded by modules.Options.MaxMemory, and the
// program that loaded it reports the memory limit.
func TestMaxMemoryBoundsAModuleTopLevel(t *testing.T) {
	loader := modules.NewLoader(modules.Map(map[string]string{
		"big.star": "BIG = ['x' * 10000 + str(i) for i in range(100000)]\n",
	}), modules.Options{MaxSteps: 5_000_000, MaxMemory: 32 << 20})
	_, err := script.Exec(context.Background(), script.Program{Source: []byte(`load("big.star", "BIG")`), Modules: loader},
		script.Options{Limits: script.Limits{MaxSteps: 5_000_000, MaxMemory: 32 << 20}})
	var se *script.Error
	if !errors.As(err, &se) || !se.MemoryExceeded || !strings.HasPrefix(se.Message, script.MemoryLimitMessage) {
		t.Fatalf("err = %v", err)
	}
}
