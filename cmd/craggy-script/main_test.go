package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, src := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func cli(args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := run(context.Background(), args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestCLI(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"lib/greet.star":      "def greet(n):\n    return 'hi ' + n\n",
		"main.star":           "load('./lib/greet.star', 'greet')\nprint('p')\ndef run(input):\n    return greet(input['who'])\n",
		"bad.star":            "x = nope\n",
		"lib/greet_test.star": "load('./greet.star', 'greet')\ndef test_greet():\n    assert.eq(greet('a'), 'hi a')\n",
		"types.go":            "package api\n\ntype In struct {\n\tWho string `json:\"who\"`\n}\n",
	})
	main := filepath.Join(dir, "main.star")

	code, out, errOut := cli("run", "-input", `{"who":"phil"}`, main)
	if code != 0 || errOut != "main.star:2:6: p\n" {
		t.Fatalf("run: %d %q %q", code, out, errOut)
	}
	var res struct {
		Value   string `json:"value"`
		Digest  string `json:"digest"`
		Modules []struct{ Path, Digest string }
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res.Value != "hi phil" || !strings.HasPrefix(res.Digest, "sha256:") || len(res.Modules) != 1 || res.Modules[0].Path != "lib/greet.star" {
		t.Fatalf("run result = %s", out)
	}

	if code, out, _ := cli("run", filepath.Join(dir, "bad.star")); code != 1 || !strings.Contains(out, `undefined: nope"`) {
		t.Fatalf("run bad: %d %s", code, out)
	}
	if code, out, _ := cli("check", filepath.Join(dir, "bad.star"), main); code != 1 || !strings.HasSuffix(out, "bad.star:1:5: error: undefined: nope\n") || strings.Count(out, "\n") != 1 {
		t.Fatalf("check: %d %q", code, out)
	}
	if code, out, _ := cli("check", "-root", dir, main); code != 0 || out != "" {
		t.Fatalf("check -root: %d %q", code, out)
	}
	if code, out, _ := cli("test", "-root", dir); code != 0 || !strings.Contains(out, "PASS lib/greet_test.star::test_greet") || !strings.Contains(out, "1 passed, 0 failed") {
		t.Fatalf("test: %d %s", code, out)
	}
	write(t, dir, map[string]string{"lib/fail_test.star": "def test_no():\n    assert.true(False)\n"})
	if code, out, _ := cli("test", "-root", dir); code != 1 || !strings.Contains(out, "FAIL lib/fail_test.star::test_no") {
		t.Fatalf("failing test: %d %s", code, out)
	}
	if code, out, _ := cli("types", filepath.Join(dir, "types.go"), "In"); code != 0 || !strings.Contains(out, `"required": [`) {
		t.Fatalf("types: %d %s", code, out)
	}
	if code, out, _ := cli("types", filepath.Join(dir, "types.go")); code != 0 || !strings.Contains(out, `"In": {`) {
		t.Fatalf("types all: %d %s", code, out)
	}
	if code, _, _ := cli("bogus"); code != 2 {
		t.Fatalf("unknown command code = %d", code)
	}
	if code, _, _ := cli("run"); code != 2 {
		t.Fatalf("run without file code = %d", code)
	}
}

// Modules are read through an os.Root: a symlink out of the module root
// does not load, for run, check or test.
func TestCLIRefusesSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	write(t, base, map[string]string{
		"secret.star":           "SECRET = 'leaked'\n",
		"root/main.star":        "load('./escape.star', 'SECRET')\ndef run():\n    return SECRET\n",
		"root/escape_test.star": "load('./escape.star', 'SECRET')\ndef test_x():\n    pass\n",
	})
	if err := os.Symlink(filepath.Join("..", "secret.star"), filepath.Join(root, "escape.star")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	if code, out, _ := cli("run", filepath.Join(root, "main.star")); code != 1 || strings.Contains(out, "leaked") || !strings.Contains(out, `"error"`) {
		t.Fatalf("run: %d %s", code, out)
	}
	if code, out, _ := cli("check", filepath.Join(root, "main.star")); code != 1 || !strings.Contains(out, "cannot read escape.star") {
		t.Fatalf("check: %d %q", code, out)
	}
	if code, out, _ := cli("test", "-root", root); code != 1 || !strings.Contains(out, "FAIL escape_test.star") {
		t.Fatalf("test: %d %s", code, out)
	}
}

// A test file's top-level print() output is shown, but is not a test.
func TestCLITestShowsTopLevelOutput(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{"p_test.star": "print('setup')\ndef test_a():\n    pass\n"})
	code, out, _ := cli("test", "-root", dir)
	if code != 0 || out != "    p_test.star:1: setup\nPASS p_test.star::test_a\n1 passed, 0 failed\n" {
		t.Fatalf("test: %d %q", code, out)
	}
}
