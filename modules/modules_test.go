package modules_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"go.starlark.net/starlark"

	"github.com/craggy-io/script-services/dialect"
	"github.com/craggy-io/script-services/modules"
)

func TestResolve(t *testing.T) {
	ok := []struct{ from, spec, want string }{
		{"lib/a.star", "./b.star", "lib/b.star"},
		{"lib/a.star", "../c.star", "c.star"},
		{"lib/x/a.star", "../../c.star", "c.star"},
		{"lib/a.star", "//x/y.star", "x/y.star"},
		{"lib/a.star", "x/y.star", "x/y.star"},
		{"lib/a.star", "x/./z/../y.star", "x/y.star"},
		{"main.star", "./lib/a.star", "lib/a.star"},
		{"", "./a.star", "a.star"},
	}
	for _, c := range ok {
		got, err := modules.Resolve(c.from, c.spec)
		if err != nil || got != c.want {
			t.Errorf("Resolve(%q, %q) = %q, %v; want %q", c.from, c.spec, got, err, c.want)
		}
	}
	bad := []struct{ from, spec, want string }{
		{"a.star", "../x.star", "escapes the module root"},
		{"lib/a.star", "../../x.star", "escapes the module root"},
		{"lib/a.star", "//../x.star", "escapes the module root"},
		{"a.star", "x/../../y.star", "escapes the module root"},
		{"a.star", "/etc/x.star", "is absolute"},
		{"a.star", "x.py", "must name a .star file"},
		{"a.star", "lib/", "must name a .star file"},
		{"a.star", "//", "escapes the module root"},
		{"a.star", "", "empty module path"},
		{"a.star", `x\y.star`, "bad module path"},
	}
	for _, c := range bad {
		if got, err := modules.Resolve(c.from, c.spec); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Resolve(%q, %q) = %q, %v; want error containing %q", c.from, c.spec, got, err, c.want)
		}
	}
}

// exec runs a program on a thread bound to l.
func exec(t *testing.T, l *modules.Loader, from, src string, pre starlark.StringDict) (starlark.StringDict, *starlark.Thread, error) {
	t.Helper()
	th := &starlark.Thread{Name: "test", Print: func(*starlark.Thread, string) {}}
	l.Bind(context.Background(), th, from)
	prog, err := dialect.Compile(from, []byte(src), pre.Has)
	if err != nil {
		return nil, th, err
	}
	g, err := prog.Init(th, pre)
	return g, th, err
}

func TestLoadRelativeAndAbsoluteAndRefs(t *testing.T) {
	src := modules.Map(map[string]string{
		"lib/text.star":  "load('./util.star', 'twice')\ndef shout(s):\n    return twice(s.upper())\n",
		"lib/util.star":  "def twice(s):\n    return s + s\n",
		"lib/other.star": "load('//lib/util.star', 'twice')\nload('../top.star', 'TOP')\nV = twice(TOP)\n",
		"top.star":       "TOP = 'x'\n",
	})
	l := modules.NewLoader(src, modules.Options{})
	g, th, err := exec(t, l, "main.star", `
load("lib/text.star", "shout")
load("./lib/other.star", "V")
out = shout("ab") + V
`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if g["out"] != starlark.String("ABABxx") {
		t.Fatalf("out = %v", g["out"])
	}
	refs := l.Refs(th)
	var paths []string
	for _, r := range refs {
		paths = append(paths, r.Path)
		if !strings.HasPrefix(r.Digest, "sha256:") || len(r.Digest) != 7+64 {
			t.Fatalf("bad digest %q", r.Digest)
		}
	}
	if got := strings.Join(paths, " "); got != "lib/other.star lib/text.star lib/util.star top.star" {
		t.Fatalf("refs = %s", got)
	}
	// Module returns the same execution and Ref.
	mg, ref, err := l.Module(context.Background(), "lib/util.star")
	if err != nil || ref != refs[2] || mg["twice"] == nil {
		t.Fatalf("Module = %v, %v, %v", mg, ref, err)
	}
	if _, _, err := l.Module(context.Background(), "./lib/util.star"); err == nil || !strings.Contains(err.Error(), "not canonical") {
		t.Fatalf("non-canonical Module err = %v", err)
	}
}

func TestDigestIsContentAddressed(t *testing.T) {
	a := "load('./b.star', 'B')\nA = B + 1\n"
	b := "B = 1\n"
	src := modules.Map(map[string]string{
		"one/a.star": a, "one/b.star": b,
		"two/deep/a.star": a, "two/deep/b.star": b,
		"three/a.star": a, "three/b.star": "B = 2\n",
	})
	l := modules.NewLoader(src, modules.Options{})
	ctx := context.Background()
	_, one, err := l.Module(ctx, "one/a.star")
	if err != nil {
		t.Fatal(err)
	}
	_, two, err := l.Module(ctx, "two/deep/a.star")
	if err != nil {
		t.Fatal(err)
	}
	_, three, err := l.Module(ctx, "three/a.star")
	if err != nil {
		t.Fatal(err)
	}
	if one.Digest != two.Digest {
		t.Fatalf("same source and deps at different paths: %s != %s", one.Digest, two.Digest)
	}
	if one.Digest == three.Digest {
		t.Fatal("digest did not change when a dependency changed")
	}
	_, bRef, _ := l.Module(ctx, "one/b.star")
	if want := modules.Digest([]byte(a), []modules.Dep{{Spec: "./b.star", Digest: bRef.Digest}}); one.Digest != want {
		t.Fatalf("digest %s, want Digest(src, deps) %s", one.Digest, want)
	}
	// A fresh loader computes the same digests.
	_, again, _ := modules.NewLoader(src, modules.Options{}).Module(ctx, "one/a.star")
	if again != one {
		t.Fatalf("digest not stable across loaders: %v vs %v", again, one)
	}
	// Dependency order does not matter; which spec loaded which digest does.
	d1 := modules.Digest([]byte("x"), []modules.Dep{{Spec: "a.star", Digest: "sha256:1"}, {Spec: "b.star", Digest: "sha256:2"}})
	d2 := modules.Digest([]byte("x"), []modules.Dep{{Spec: "b.star", Digest: "sha256:2"}, {Spec: "a.star", Digest: "sha256:1"}})
	if d1 != d2 {
		t.Fatal("Digest depends on dependency order")
	}
	if d3 := modules.Digest([]byte("x"), []modules.Dep{{Spec: "a.star", Digest: "sha256:2"}, {Spec: "b.star", Digest: "sha256:1"}}); d3 == d1 {
		t.Fatal("Digest ignores which spec loaded which digest")
	}
	// Length prefixes keep spec and digest boundaries apart.
	if modules.Digest([]byte("x"), []modules.Dep{{Spec: "ab", Digest: "c"}}) == modules.Digest([]byte("x"), []modules.Dep{{Spec: "a", Digest: "bc"}}) {
		t.Fatal("Digest confuses spec and digest boundaries")
	}
	if modules.Digest([]byte("x"), nil) == modules.Digest([]byte("y"), nil) {
		t.Fatal("Digest ignores source")
	}
}

func TestCycles(t *testing.T) {
	src := modules.Map(map[string]string{
		"a.star":    "load('b.star', 'B')\nA = 1\n",
		"b.star":    "load('c.star', 'C')\nB = 1\n",
		"c.star":    "load('a.star', 'A')\nC = 1\n",
		"self.star": "load('./self.star', 'X')\nX = 1\n",
	})
	l := modules.NewLoader(src, modules.Options{})
	_, _, err := l.Module(context.Background(), "a.star")
	if err == nil || !strings.Contains(err.Error(), "load cycle: a.star -> b.star -> c.star -> a.star") {
		t.Fatalf("cycle err = %v", err)
	}
	var ce *modules.CycleError
	if !errors.As(err, &ce) || len(ce.Chain) != 4 {
		t.Fatalf("no CycleError in %v", err)
	}
	_, _, err = l.Module(context.Background(), "self.star")
	if err == nil || !strings.Contains(err.Error(), "load cycle: self.star -> self.star") {
		t.Fatalf("self cycle err = %v", err)
	}
}

// Two goroutines entering a cycle from opposite ends must fail, not
// deadlock.
func TestConcurrentCycleDoesNotDeadlock(t *testing.T) {
	var arrived sync.WaitGroup
	arrived.Add(2)
	release := make(chan struct{})
	pre := starlark.StringDict{"gate": starlark.NewBuiltin("gate", func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
		arrived.Done()
		<-release
		return starlark.None, nil
	})}
	src := modules.Map(map[string]string{
		"a.star": "gate()\nload('b.star', 'B')\nA = 1\n",
		"b.star": "gate()\nload('a.star', 'A')\nB = 1\n",
	})
	l := modules.NewLoader(src, modules.Options{Predeclared: pre})
	errs := make(chan error, 2)
	for _, p := range []string{"a.star", "b.star"} {
		go func() {
			_, _, err := l.Module(context.Background(), p)
			errs <- err
		}()
	}
	arrived.Wait()
	close(release)
	for range 2 {
		select {
		case err := <-errs:
			if err == nil || !strings.Contains(err.Error(), "load cycle") {
				t.Fatalf("err = %v, want a load cycle", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("deadlock: concurrent cycle never resolved")
		}
	}
}

// countingSource counts reads per path.
type countingSource struct {
	modules.Source
	mu    sync.Mutex
	reads map[string]int
}

func (c *countingSource) Read(ctx context.Context, p string) ([]byte, error) {
	c.mu.Lock()
	c.reads[p]++
	c.mu.Unlock()
	return c.Source.Read(ctx, p)
}

func TestConcurrentLoadsCoalesce(t *testing.T) {
	var runs atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	pre := starlark.StringDict{"slow": starlark.NewBuiltin("slow", func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
		if runs.Add(1) == 1 {
			close(started)
		}
		<-release
		return starlark.None, nil
	})}
	src := &countingSource{Source: modules.Map(map[string]string{"slow.star": "slow()\nV = [1, 2]\n"}), reads: map[string]int{}}
	l := modules.NewLoader(src, modules.Options{Predeclared: pre})
	const n = 8
	var wg sync.WaitGroup
	got := make([]starlark.Value, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g, _, err := l.Module(context.Background(), "slow.star")
			if err == nil {
				got[i] = g["V"]
			}
			errs[i] = err
		}()
	}
	<-started
	time.Sleep(50 * time.Millisecond) // let the others queue behind the first
	close(release)
	wg.Wait()
	for i := range n {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if got[i] != got[0] {
			t.Fatal("loads did not share one execution")
		}
	}
	if runs.Load() != 1 || src.reads["slow.star"] != 1 {
		t.Fatalf("runs = %d, reads = %d; want 1 each", runs.Load(), src.reads["slow.star"])
	}
	// Globals are frozen.
	if err := got[0].(*starlark.List).Append(starlark.MakeInt(3)); err == nil {
		t.Fatal("module globals are not frozen")
	}
}

func TestCancellationIsNotCached(t *testing.T) {
	var done atomic.Bool
	var runs atomic.Int32
	pre := starlark.StringDict{
		"done": starlark.NewBuiltin("done", func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
			return starlark.Bool(done.Load()), nil
		}),
		"count": starlark.NewBuiltin("count", func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
			runs.Add(1)
			return starlark.None, nil
		}),
	}
	src := modules.Map(map[string]string{"spin.star": "count()\nwhile not done():\n    pass\nV = 1\n"})
	l := modules.NewLoader(src, modules.Options{Predeclared: pre})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, _, err := l.Module(ctx, "spin.star")
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("cancelled load err = %v", err)
	}
	var me *modules.Error
	if !errors.As(err, &me) || me.Path != "spin.star" || me.Pos.Line == 0 {
		t.Fatalf("err %v lacks module position", err)
	}
	done.Store(true)
	g, _, err := l.Module(context.Background(), "spin.star")
	if err != nil || g["V"] != starlark.MakeInt(1) || runs.Load() != 2 {
		t.Fatalf("reload after cancel: %v, %v, runs=%d", g, err, runs.Load())
	}
}

func TestStepLimitAndErrorPositions(t *testing.T) {
	src := modules.Map(map[string]string{
		"loop.star":   "n = 0\nwhile True:\n    n += 1\n",
		"div.star":    "def f():\n    return 1 // 0\nX = f()\n",
		"syntax.star": "x = (\n",
		"undef.star":  "x = nope\n",
		"user.star":   "load('div.star', 'X')\n",
	})
	l := modules.NewLoader(src, modules.Options{MaxSteps: 1000})
	ctx := context.Background()
	cases := map[string]string{
		"loop.star":   "loop.star:3:7: Starlark computation cancelled: too many steps",
		"div.star":    "div.star:2:14: floored division by zero",
		"syntax.star": "syntax.star:2:1: got end of file, want primary expression",
		"undef.star":  "undef.star:1:5: undefined: nope",
		"user.star":   "user.star:1:1: cannot load div.star: div.star:2:14: floored division by zero",
		"none.star":   "none.star: read none.star: file does not exist",
	}
	for p, want := range cases {
		_, _, err := l.Module(ctx, p)
		if err == nil || err.Error() != want {
			t.Errorf("%s: err = %v\nwant %s", p, err, want)
		}
	}
	_, _, err := l.Module(ctx, "none.star")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing module err %v does not wrap fs.ErrNotExist", err)
	}
	// Failures are cached like successes.
	_, _, err2 := l.Module(ctx, "div.star")
	_, _, err3 := l.Module(ctx, "div.star")
	if err2 != err3 {
		t.Error("deterministic failure was not cached")
	}
}

func TestDirSourcePredeclaredAndPrint(t *testing.T) {
	fsys := fstest.MapFS{
		"lib/m.star": {Data: []byte("print('loading')\nV = host()\n")},
	}
	pre := starlark.StringDict{"host": starlark.NewBuiltin("host", func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
		return starlark.String("h"), nil
	})}
	l := modules.NewLoader(modules.Dir(fsys), modules.Options{Predeclared: pre})
	var printed []string
	th := &starlark.Thread{Print: func(th *starlark.Thread, msg string) {
		printed = append(printed, th.CallFrame(1).Pos.String()+" "+msg)
	}}
	l.Bind(context.Background(), th, "main.star")
	prog, err := dialect.Compile("main.star", []byte("load('lib/m.star', 'V')\nout = V\n"), func(string) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	g, err := prog.Init(th, nil)
	if err != nil || g["out"] != starlark.String("h") {
		t.Fatalf("out = %v, %v", g["out"], err)
	}
	if len(printed) != 1 || printed[0] != "lib/m.star:1:6 loading" {
		t.Fatalf("printed = %q", printed)
	}
	if _, err := modules.Dir(fsys).Read(context.Background(), "/abs.star"); err == nil {
		t.Fatal("Dir accepted an invalid path")
	}
}

// Swapping the contents of two dependencies loaded under different specs
// changes behaviour, so it must change the digest: the digest pairs each
// load spec with the digest it resolved to.
func TestDigestSwapCollision(t *testing.T) {
	main := "load('x.star', X = 'V')\nload('y.star', Y = 'V')\nOUT = X - Y\n"
	ctx := context.Background()
	load := func(x, y string) (starlark.Value, modules.Ref) {
		t.Helper()
		l := modules.NewLoader(modules.Map(map[string]string{"main.star": main, "x.star": x, "y.star": y}), modules.Options{})
		g, ref, err := l.Module(ctx, "main.star")
		if err != nil {
			t.Fatal(err)
		}
		return g["OUT"], ref
	}
	out1, ref1 := load("V = 1\n", "V = 2\n")
	out2, ref2 := load("V = 2\n", "V = 1\n")
	if out1 == out2 {
		t.Fatalf("fixture does not change behaviour: %v == %v", out1, out2)
	}
	if ref1.Digest == ref2.Digest {
		t.Fatalf("swapped dependencies share digest %s but compute %v and %v", ref1.Digest, out1, out2)
	}
}

// symlinkTree builds root/{ok.star, inner.star -> ok.star, escape.star ->
// ../outside/secret.star, out -> ../outside} and returns root.
func symlinkTree(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	root, outside := filepath.Join(base, "root"), filepath.Join(base, "outside")
	for _, d := range []string{root, outside} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "ok.star"), "V = 'inside'\n")
	write(filepath.Join(outside, "secret.star"), "V = 'outside'\n")
	links := map[string]string{
		"inner.star":  "ok.star",
		"escape.star": filepath.Join("..", "outside", "secret.star"),
		"out":         filepath.Join("..", "outside"),
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Skipf("symlinks unsupported: %v", err)
		}
	}
	return root
}

func TestDirRootRejectsSymlinkEscape(t *testing.T) {
	root := symlinkTree(t)
	ctx := context.Background()
	// The hazard DirRoot exists for: os.DirFS follows the link out.
	if b, err := modules.Dir(os.DirFS(root)).Read(ctx, "escape.star"); err != nil || !strings.Contains(string(b), "outside") {
		t.Fatalf("os.DirFS fixture: %q, %v", b, err)
	}
	src, err := modules.DirRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if b, err := src.Read(ctx, "ok.star"); err != nil || string(b) != "V = 'inside'\n" {
		t.Fatalf("ok.star = %q, %v", b, err)
	}
	if b, err := src.Read(ctx, "inner.star"); err != nil || string(b) != "V = 'inside'\n" {
		t.Fatalf("symlink inside the root = %q, %v", b, err)
	}
	for _, p := range []string{"escape.star", "out/secret.star"} {
		if b, err := src.Read(ctx, p); err == nil {
			t.Fatalf("Read(%q) escaped the root: %q", p, b)
		}
	}
	// Through a Loader, the escape is a module error naming the module.
	l := modules.NewLoader(src, modules.Options{})
	if _, _, err := l.Module(ctx, "escape.star"); err == nil {
		t.Fatal("Loader loaded a module outside the root")
	}
	if _, err := fs.ReadFile(src.FS(), "ok.star"); err != nil {
		t.Fatalf("FS: %v", err)
	}
	if err := src.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Read(ctx, "ok.star"); err == nil {
		t.Fatal("Read after Close succeeded")
	}
	if _, err := modules.DirRoot(filepath.Join(root, "missing")); err == nil {
		t.Fatal("DirRoot of a missing directory succeeded")
	}
}
