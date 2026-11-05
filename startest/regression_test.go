package startest_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.starlark.net/starlark"

	"github.com/craggy-io/script-services/startest"
)

var errHostFail = errors.New("host failure")

func runOne(t *testing.T, src string, opts startest.Options) []startest.Outcome {
	t.Helper()
	fsys := bundleFS(xManifest, map[string]string{"x_test.star": src})
	return startest.RunUnit(context.Background(), unit(t, fsys, "x", opts), opts)
}

// A test file with no test_* functions used to return zero outcomes and
// pass everywhere.
func TestRunUnitNoTestsFails(t *testing.T) {
	out := runOne(t, "x = 1\n", startest.Options{})
	if len(out) != 1 || out[0].Err == nil || out[0].Test != "" {
		t.Fatalf("no-tests outcomes = %+v", out)
	}
	if !strings.Contains(out[0].Err.Message, "no test_* functions") {
		t.Fatalf("no-tests message = %q", out[0].Err.Message)
	}
}

func TestRunUnitNonFunctionAndBuiltinTests(t *testing.T) {
	out := runOne(t, "test_stringy = 'nope'\n", startest.Options{})
	if len(out) != 1 || out[0].Err == nil || out[0].Test != "test_stringy" {
		t.Fatalf("stringy outcomes = %+v", out)
	}

	hostFail := starlark.NewBuiltin("hostfail", func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
		return nil, errHostFail
	})
	out = runOne(t, "test_builtin = hostfail\n", startest.Options{
		Predeclared: starlark.StringDict{"hostfail": hostFail},
	})
	if len(out) != 1 || out[0].Err == nil || out[0].Test != "test_builtin" {
		t.Fatalf("builtin outcomes = %+v", out)
	}
}

// The documented keyword forms (got=/want=/cond=) must unpack.
func TestAssertDocumentedKwargs(t *testing.T) {
	out := runOne(t, "def test_kwargs():\n    assert.eq(got = 1, want = 1)\n    assert.ne(got = 1, other = 2)\n    assert.true(cond = True)\n", startest.Options{})
	if len(out) != 1 || !out[0].Passed() {
		t.Fatalf("kwargs outcomes = %+v", out)
	}
}
