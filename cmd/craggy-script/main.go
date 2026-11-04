// Command craggy-script checks, runs and tests Craggy Starlark scripts, and
// prints the JSON Schemas of Go-syntax type declarations.
//
//	craggy-script check [-root DIR] FILE...
//	craggy-script run [-input JSON] [-root DIR] FILE
//	craggy-script test [-root DIR]
//	craggy-script types FILE.go [NAME]
//
// check prints one diagnostic per line, with only `input` predeclared, and
// checks load() targets under DIR (default: each FILE's directory). run
// executes FILE with load() served from DIR (default: FILE's directory) and
// prints the result as JSON, including its digest and the modules it loaded.
// test runs every *_test.star file under DIR (default: the current
// directory), printing a file's top-level print() output before its tests.
// types prints the schema of NAME, or of every declared type.
//
// DIR is opened with modules.DirRoot: modules are read through an os.Root,
// so a symbolic link pointing out of DIR cannot be loaded.
//
// Exit status is 0 on success, 1 when a check, run or test fails, and 2 on
// usage errors.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/craggy-io/script-services/check"
	"github.com/craggy-io/script-services/modules"
	"github.com/craggy-io/script-services/script"
	"github.com/craggy-io/script-services/startest"
	"github.com/craggy-io/script-services/types"
)

const usage = `usage:
  craggy-script check [-root DIR] FILE...
  craggy-script run [-input JSON] [-root DIR] FILE
  craggy-script test [-root DIR]
  craggy-script types FILE.go [NAME]
`

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	var err error
	code := 0
	switch args[0] {
	case "check":
		code, err = cmdCheck(args[1:], stdout, stderr)
	case "run":
		code, err = cmdRun(ctx, args[1:], stdout, stderr)
	case "test":
		code, err = cmdTest(ctx, args[1:], stdout, stderr)
	case "types":
		code, err = cmdTypes(args[1:], stdout, stderr)
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "craggy-script: unknown command %q\n%s", args[0], usage)
		return 2
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(stderr, "craggy-script %s: %v\n", args[0], err)
		if code == 0 {
			code = 1
		}
	}
	return code
}

var errUsage = errors.New("bad usage")

func newFlags(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

func cmdCheck(args []string, stdout, stderr io.Writer) (int, error) {
	fs := newFlags("check", stderr)
	root := fs.String("root", "", "module root for load() targets (default: each file's directory)")
	if err := fs.Parse(args); err != nil {
		return 2, err
	}
	if fs.NArg() == 0 {
		return 2, fmt.Errorf("%w: no files", errUsage)
	}
	code := 0
	for _, file := range fs.Args() {
		src, err := os.ReadFile(file)
		if err != nil {
			return 1, err
		}
		dir := *root
		if dir == "" {
			dir = filepath.Dir(file)
		}
		name, err := relTo(dir, file)
		if err != nil {
			return 2, err
		}
		mods, err := modules.DirRoot(dir)
		if err != nil {
			return 1, err
		}
		diags := check.Check(check.Env{Modules: mods}, name, src)
		mods.Close()
		for _, d := range diags {
			shown := d.Pos.File
			if shown == name {
				shown = file
			}
			fmt.Fprintf(stdout, "%s:%d:%d: %s: %s\n", shown, d.Pos.Line, d.Pos.Col, d.Severity, d.Message)
			if d.Severity == "error" {
				code = 1
			}
		}
	}
	return code, nil
}

// relTo returns file's slash path relative to root, which must contain it.
func relTo(root, file string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absFile, err := filepath.Abs(file)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absRoot, absFile)
	if err != nil {
		return "", err
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("%s is not under -root %s", file, root)
	}
	return rel, nil
}

func cmdRun(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs := newFlags("run", stderr)
	input := fs.String("input", "null", "JSON input bound to `input`")
	root := fs.String("root", "", "module root for load() (default: the file's directory)")
	if err := fs.Parse(args); err != nil {
		return 2, err
	}
	if fs.NArg() != 1 {
		return 2, fmt.Errorf("%w: want exactly one FILE", errUsage)
	}
	file := fs.Arg(0)
	if *root == "" {
		*root = filepath.Dir(file)
	}
	var in any
	if err := json.Unmarshal([]byte(*input), &in); err != nil {
		return 2, fmt.Errorf("-input: %w", err)
	}
	name, err := relTo(*root, file)
	if err != nil {
		return 2, err
	}
	src, err := os.ReadFile(file)
	if err != nil {
		return 1, err
	}
	mods, err := modules.DirRoot(*root)
	if err != nil {
		return 1, err
	}
	defer mods.Close()
	lim := script.DefaultLimits
	loader := modules.NewLoader(mods, modules.Options{MaxSteps: lim.MaxSteps})
	res, err := script.Exec(ctx, script.Program{Filename: name, Source: src, Input: in, Modules: loader}, script.Options{
		Limits: lim,
		Print: func(pos script.Position, msg string) {
			fmt.Fprintf(stderr, "%s:%d:%d: %s\n", pos.File, pos.Line, pos.Col, msg)
		},
	})
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err != nil {
		var se *script.Error
		if errors.As(err, &se) {
			_ = enc.Encode(map[string]any{"error": se})
			return 1, nil
		}
		return 1, err
	}
	return 0, enc.Encode(res)
}

func cmdTest(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs := newFlags("test", stderr)
	root := fs.String("root", ".", "directory to search for *_test.star files; also the module root")
	if err := fs.Parse(args); err != nil {
		return 2, err
	}
	if fs.NArg() != 0 {
		return 2, fmt.Errorf("%w: unexpected arguments %v", errUsage, fs.Args())
	}
	src, err := modules.DirRoot(*root)
	if err != nil {
		return 1, err
	}
	defer src.Close()
	files, err := startest.Files(src.FS())
	if err != nil {
		return 1, err
	}
	if len(files) == 0 {
		return 1, fmt.Errorf("no *_test.star files under %s", *root)
	}
	passed, failed := 0, 0
	for _, f := range files {
		for _, o := range startest.RunFile(ctx, src, f, startest.Options{}) {
			for _, line := range o.Output {
				fmt.Fprintf(stdout, "    %s\n", line)
			}
			if o.Test == "" && o.Passed() {
				continue // the file's top-level print() output; not a test
			}
			if o.Passed() {
				passed++
				fmt.Fprintf(stdout, "PASS %s\n", o.Name())
				continue
			}
			failed++
			fmt.Fprintf(stdout, "FAIL %s\n    %s\n", o.Name(), o.Err.Error())
		}
	}
	fmt.Fprintf(stdout, "%d passed, %d failed\n", passed, failed)
	if failed > 0 {
		return 1, nil
	}
	return 0, nil
}

func cmdTypes(args []string, stdout, stderr io.Writer) (int, error) {
	fs := newFlags("types", stderr)
	if err := fs.Parse(args); err != nil {
		return 2, err
	}
	if fs.NArg() < 1 || fs.NArg() > 2 {
		return 2, fmt.Errorf("%w: want FILE.go [NAME]", errUsage)
	}
	src, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return 1, err
	}
	pkg, err := types.Parse(fs.Arg(0), src)
	if err != nil {
		return 1, err
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if fs.NArg() == 2 {
		s, err := pkg.Schema(fs.Arg(1))
		if err != nil {
			return 1, err
		}
		return 0, enc.Encode(s)
	}
	all := map[string]*jsonschema.Schema{}
	for _, name := range pkg.Names() {
		s, err := pkg.Schema(name)
		if err != nil {
			return 1, err
		}
		all[name] = s
	}
	return 0, enc.Encode(all)
}
