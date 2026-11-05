package main

import (
	"strings"
	"testing"
)

// -input must reach the script byte-exact: integers past float64's exact
// range used to be silently rounded.
func TestCLIRunInputKeepsBigIntegers(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"bundle.yaml": "apiVersion: craggy.io/bundle/v1\nmetadata: {id: demo.big, version: 1.0.0}\ncontributions:\n  - {id: main, kind: craggy.script@1, path: main.star}\n",
		"main.star":   "def run(input):\n    return input[\"id\"]\n",
	})
	code, out, errOut := cli("run", "-input", `{"id":9007199254740993}`, dir)
	if code != 0 || !strings.Contains(out, "9007199254740993") {
		t.Fatalf("run big input: %d %s %s", code, out, errOut)
	}
}

// One bad script must not hide the problems of another: check reports
// every problem in the bundle, and #ID narrows the report to one script.
func TestCLICheckReportsEveryProblem(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"bundle.yaml": "apiVersion: craggy.io/bundle/v1\nmetadata: {id: demo.bad, version: 1.0.0}\ncontributions:\n  - {id: a, kind: craggy.script@1, path: a.star}\n  - {id: b, kind: craggy.script@1, path: b.star}\n  - {id: c, kind: craggy.script@1, path: missing.star}\n",
		"a.star":      "x = nope\n",
		"b.star":      "load('gone.star', 'y')\n",
	})
	code, out, _ := cli("check", dir)
	if code != 1 || !strings.Contains(out, "a.star:1:5: error: undefined: nope") || !strings.Contains(out, "b.star:1:6: error: load \"gone.star\"") || !strings.Contains(out, "missing.star") {
		t.Fatalf("check: %d %s", code, out)
	}
	code, out, _ = cli("check", dir+"#a")
	if code != 1 || !strings.Contains(out, "a.star") || strings.Contains(out, "b.star") {
		t.Fatalf("check #a: %d %s", code, out)
	}
}

// A test file that defines no tests fails, not "0 passed, 0 failed" with
// exit 0; and failures show their backtrace frames.
func TestCLITestZeroTestsAndBacktrace(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"bundle.yaml":     "apiVersion: craggy.io/bundle/v1\nmetadata: {id: demo.deep, version: 1.0.0}\ncontributions:\n  - {id: empty, kind: craggy.script-test@1, path: empty_test.star}\n  - {id: deep, kind: craggy.script-test@1, path: deep_test.star}\n",
		"empty_test.star": "x = 1\n",
		"lib.star":        "def boom():\n    return {}[\"deep\"]\n",
		"deep_test.star":  "load('./lib.star', 'boom')\ndef test_deep():\n    boom()\n",
	})
	code, out, _ := cli("test", dir)
	if code == 0 || !strings.Contains(out, "no test_* functions") {
		t.Fatalf("zero-test file: %d %s", code, out)
	}
	if !strings.Contains(out, "at boom (lib.star:") {
		t.Fatalf("backtrace missing lib frame: %s", out)
	}
}
