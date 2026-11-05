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

// write writes files under dir. A bundle's files include its hand-written
// bundle.yaml: nothing generates one.
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

const appManifest = `apiVersion: craggy.io/bundle/v1
metadata: {id: demo.app, version: 1.0.0}
contributions:
  - {id: main, kind: craggy.script@1, path: main.star}
  - {id: greet-test, kind: craggy.script-test@1, path: lib/greet_test.star}
  - {id: api, kind: craggy.types@1, path: types.go}
`

func TestCLI(t *testing.T) {
	base := t.TempDir()
	app := filepath.Join(base, "app")
	write(t, app, map[string]string{
		"bundle.yaml":           appManifest,
		"lib/greet.star":        "def greet(n):\n    return 'hi ' + n\n",
		"main.star":             "load('./lib/greet.star', 'greet')\nprint('p')\ndef run(input):\n    return greet(input['who'])\n",
		"lib/greet_test.star":   "load('./greet.star', 'greet')\ndef test_greet():\n    assert.eq(greet('a'), 'hi a')\n",
		"lib/ignored_test.star": "def test_never():\n    assert.true(False)\n", // not declared, so not a test
		"types.go":              "package api\n\ntype In struct {\n\tWho string `json:\"who\"`\n}\n",
	})
	broken := filepath.Join(base, "broken")
	write(t, broken, map[string]string{
		"bundle.yaml": "apiVersion: craggy.io/bundle/v1\nmetadata: {id: demo.broken, version: 1.0.0}\ncontributions:\n  - {id: bad, kind: craggy.script@1, path: bad.star}\n",
		"bad.star":    "x = nope\n",
	})

	code, out, errOut := cli("run", "-input", `{"who":"phil"}`, app+"#main")
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
	// With one script in the bundle, #ID is optional.
	if code, out, _ := cli("run", "-input", `{"who":"x"}`, app); code != 0 || !strings.Contains(out, `"hi x"`) {
		t.Fatalf("run without #ID: %d %s", code, out)
	}
	if code, _, errOut := cli("run", app+"#nope"); code != 2 || !strings.Contains(errOut, `no craggy.script@1 contribution "nope"`) {
		t.Fatalf("run unknown ID: %d %s", code, errOut)
	}

	if code, out, _ := cli("run", broken); code != 1 || out != "demo.broken: bad.star:1:5: error: undefined: nope (script.check)\n" {
		t.Fatalf("run broken: %d %q", code, out)
	}
	if code, out, _ := cli("check", broken); code != 1 || out != "demo.broken: bad.star:1:5: error: undefined: nope (script.check)\n" {
		t.Fatalf("check broken: %d %q", code, out)
	}
	if code, out, _ := cli("check", app); code != 0 || out != "" {
		t.Fatalf("check app: %d %q", code, out)
	}
	if code, out, _ := cli("check", app+"#greet-test"); code != 0 || out != "" {
		t.Fatalf("check app#greet-test: %d %q", code, out)
	}
	if code, out, _ := cli("test", app); code != 0 || !strings.Contains(out, "PASS lib/greet_test.star::test_greet") || !strings.Contains(out, "1 passed, 0 failed") {
		t.Fatalf("test: %d %s", code, out)
	}
	write(t, app, map[string]string{
		"bundle.yaml":        appManifest + "  - {id: fail-test, kind: craggy.script-test@1, path: lib/fail_test.star}\n",
		"lib/fail_test.star": "def test_no():\n    assert.true(False)\n",
	})
	if code, out, _ := cli("test", app); code != 1 || !strings.Contains(out, "FAIL lib/fail_test.star::test_no") {
		t.Fatalf("failing test: %d %s", code, out)
	}
	if code, out, _ := cli("test", app+"#greet-test"); code != 0 || strings.Contains(out, "fail_test") {
		t.Fatalf("test #ID: %d %s", code, out)
	}
	if code, out, _ := cli("types", app, "In"); code != 0 || !strings.Contains(out, `"required": [`) {
		t.Fatalf("types: %d %s", code, out)
	}
	if code, out, _ := cli("types", app+"#api"); code != 0 || !strings.Contains(out, `"In": {`) {
		t.Fatalf("types all: %d %s", code, out)
	}
	if code, _, _ := cli("bogus"); code != 2 {
		t.Fatalf("unknown command code = %d", code)
	}
	if code, _, _ := cli("run"); code != 2 {
		t.Fatalf("run without bundle code = %d", code)
	}
}

// A file or a folder without bundle.yaml is not a bundle.
func TestCLIRequiresBundle(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{"main.star": "def run():\n    return 1\n"})
	if code, out, _ := cli("run", dir); code != 1 || !strings.Contains(out, "(bundle.manifest_missing)") {
		t.Fatalf("run folder: %d %q", code, out)
	}
	if code, out, errOut := cli("check", filepath.Join(dir, "main.star")); code != 1 || out+errOut == "" {
		t.Fatalf("check file: %d %q %q", code, out, errOut)
	}
}

// Dependencies are other bundles, given with -dep and declared in
// bundle.yaml; scripts load their files as @<dependency-id>/path.
func TestCLIDependency(t *testing.T) {
	base := t.TempDir()
	write(t, base, map[string]string{
		"lib/bundle.yaml":    "apiVersion: craggy.io/bundle/v1\nmetadata: {id: demo.lib, version: 1.0.0}\ncontributions: []\n",
		"lib/text/up.star":   "load('./help.star', 'bang')\ndef up(s):\n    return bang(s.upper())\n",
		"lib/text/help.star": "def bang(s):\n    return s + '!'\n",
		"app/bundle.yaml":    "apiVersion: craggy.io/bundle/v1\nmetadata: {id: demo.app, version: 1.0.0}\ndependencies:\n  - id: demo.lib\ncontributions:\n  - {id: main, kind: craggy.script@1, path: main.star}\n",
		"app/main.star":      "load('@demo.lib/text/up.star', 'up')\ndef run():\n    return up('hi')\n",
	})
	code, out, _ := cli("run", "-dep", filepath.Join(base, "lib"), filepath.Join(base, "app"))
	if code != 0 || !strings.Contains(out, `"HI!"`) || !strings.Contains(out, `"@demo.lib/text/help.star"`) {
		t.Fatalf("run with -dep: %d %s", code, out)
	}
	if code, out, _ := cli("run", filepath.Join(base, "app")); code != 1 || !strings.Contains(out, "load alias @demo.lib") {
		t.Fatalf("run without -dep: %d %s", code, out)
	}
}

// Bundles may not contain symbolic links, so a link out of the bundle
// cannot be loaded, for run, check or test.
func TestCLIRefusesSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	write(t, base, map[string]string{
		"secret.star":           "SECRET = 'leaked'\n",
		"root/bundle.yaml":      "apiVersion: craggy.io/bundle/v1\nmetadata: {id: demo.escape, version: 1.0.0}\ncontributions:\n  - {id: main, kind: craggy.script@1, path: main.star}\n  - {id: escape-test, kind: craggy.script-test@1, path: escape_test.star}\n",
		"root/main.star":        "load('./escape.star', 'SECRET')\ndef run():\n    return SECRET\n",
		"root/escape_test.star": "load('./escape.star', 'SECRET')\ndef test_x():\n    pass\n",
	})
	if err := os.Symlink(filepath.Join("..", "secret.star"), filepath.Join(root, "escape.star")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	for _, cmd := range []string{"run", "check", "test"} {
		if code, out, _ := cli(cmd, root); code != 1 || strings.Contains(out, "leaked") || !strings.Contains(out, "(bundle.symlink_forbidden)") {
			t.Fatalf("%s: %d %s", cmd, code, out)
		}
	}
}

// A test file's top-level print() output is shown, but is not a test.
func TestCLITestShowsTopLevelOutput(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"bundle.yaml": "apiVersion: craggy.io/bundle/v1\nmetadata: {id: demo.print, version: 1.0.0}\ncontributions:\n  - {id: p, kind: craggy.script-test@1, path: p_test.star}\n",
		"p_test.star": "print('setup')\ndef test_a():\n    pass\n",
	})
	code, out, _ := cli("test", dir)
	if code != 0 || out != "    p_test.star:1: setup\nPASS p_test.star::test_a\n1 passed, 0 failed\n" {
		t.Fatalf("test: %d %q", code, out)
	}
}
