package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// -input must reach the script byte-exact: integers past float64's exact
// range used to be silently rounded.
func TestCLIRunInputKeepsBigIntegers(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"main.star": "def run(input):\n    return input[\"id\"]\n",
	})
	code, out, errOut := cli("run", "-input", `{"id":9007199254740993}`, filepath.Join(dir, "main.star"))
	if code != 0 || !strings.Contains(out, "9007199254740993") {
		t.Fatalf("run big input: %d %s %s", code, out, errOut)
	}
}

// One unreadable file must not stop later files from being checked.
func TestCLICheckContinuesAfterBadFile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{"bad.star": "x = nope\n"})
	code, out, _ := cli("check", filepath.Join(dir, "missing.star"), filepath.Join(dir, "bad.star"))
	if code == 0 || !strings.Contains(out, "undefined: nope") {
		t.Fatalf("check batch: %d %s", code, out)
	}
}

// A directory whose only test file defines no tests fails, not "0 passed,
// 0 failed" with exit 0; and failures show their backtrace frames.
func TestCLITestZeroTestsAndBacktrace(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"empty_test.star": "x = 1\n",
		"lib.star":        "def boom():\n    return {}[\"deep\"]\n",
		"deep_test.star":  "load('./lib.star', 'boom')\ndef test_deep():\n    boom()\n",
	})
	code, out, _ := cli("test", "-root", dir)
	if code == 0 || !strings.Contains(out, "no test_* functions") {
		t.Fatalf("zero-test file: %d %s", code, out)
	}
	if !strings.Contains(out, "at boom (lib.star:") {
		t.Fatalf("backtrace missing lib frame: %s", out)
	}
}
