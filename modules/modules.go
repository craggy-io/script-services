// Package modules implements load() for Craggy scripts: it resolves module
// paths, reads module source, executes each module once, and gives every
// result a content address.
//
// Paths. A module is named by a canonical path: slash-separated, relative to
// the module root, cleaned, ending in ".star" (for example "lib/text.star").
// In load(), "./x.star" and "../x.star" are relative to the loading file,
// while "//lib/x.star" and a bare "lib/x.star" are relative to the root.
// Paths that would escape the root are rejected (see Resolve).
//
// Execution. A Loader executes each module at most once, in the Craggy
// dialect, with its predeclared names, on a child thread of its own bounded
// by Options.MaxSteps and cancelled when the load's context ends. The
// module's globals are frozen and cached; concurrent loads of one path share
// one execution. A load cycle is an error naming the chain
// ("a.star -> b.star -> a.star"). Failures are *Error values carrying the
// failing module's path and position. A load cut short by its context is not
// cached: a later load runs the module again.
//
// Content addresses. Every loaded module has a Ref whose Digest covers its
// source and, for each load() statement, the spec as written paired with the
// digest of the module it resolved to (see Digest). Its path is not covered:
// the same code with the same dependencies has the same digest wherever it
// lives, and a change anywhere below a module changes its digest. A program
// run by package script gets its digest by the same formula, so one file has
// one digest whether it runs as a program or loads as a module.
//
// Sources. DirRoot reads modules from a directory and is the Source to use
// for a directory on disk: it opens the directory as an os.Root, so neither
// ".." nor a symbolic link reaches outside it. Dir(os.DirFS(root)) follows
// symbolic links out of the root; keep Dir for an embed.FS, an fstest.MapFS
// or another fs.FS that cannot escape.
package modules

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"sync"

	"go.starlark.net/resolve"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/craggy-io/script-services/dialect"
)

// Source reads module source by canonical path.
type Source interface {
	// Read returns the source of the module at path, which is canonical:
	// slash-separated, no leading slash, cleaned. A missing module is an
	// error wrapping fs.ErrNotExist.
	Read(ctx context.Context, path string) ([]byte, error)
}

// Dir is a Source reading modules from a file system rooted at the module
// root, such as an embed.FS. For a directory on disk use DirRoot instead:
// os.DirFS follows symbolic links, including ones pointing out of the root.
func Dir(fsys fs.FS) Source { return dirSource{fsys} }

// RootSource is a Source reading modules from a directory on disk through an
// os.Root, which rejects any path, ".." or symbolic link that would leave
// the directory. It holds the directory open: Close it when no Loader or
// check needs it any more. It is safe for concurrent use.
type RootSource struct {
	root *os.Root
	fsys fs.FS
}

// DirRoot opens dir as the module root. Reads that would escape it through a
// symbolic link fail, as do all reads after Close.
func DirRoot(dir string) (*RootSource, error) {
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &RootSource{root: r, fsys: r.FS()}, nil
}

// Read returns the source of the module at the canonical path p.
func (s *RootSource) Read(ctx context.Context, p string) ([]byte, error) {
	return dirSource{s.fsys}.Read(ctx, p)
}

// FS returns the root as an fs.FS confined the same way, for walking it (for
// example with startest.Files).
func (s *RootSource) FS() fs.FS { return s.fsys }

// Close releases the directory.
func (s *RootSource) Close() error { return s.root.Close() }

type dirSource struct{ fsys fs.FS }

func (d dirSource) Read(ctx context.Context, p string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !fs.ValidPath(p) {
		return nil, &fs.PathError{Op: "read", Path: p, Err: fs.ErrInvalid}
	}
	return fs.ReadFile(d.fsys, p)
}

// Map is a Source serving modules from memory, keyed by path.
func Map(files map[string]string) Source {
	m := make(mapSource, len(files))
	for k, v := range files {
		m[path.Clean(strings.TrimPrefix(k, "/"))] = v
	}
	return m
}

type mapSource map[string]string

func (m mapSource) Read(ctx context.Context, p string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	src, ok := m[p]
	if !ok {
		return nil, &fs.PathError{Op: "read", Path: p, Err: fs.ErrNotExist}
	}
	return []byte(src), nil
}

// Ref identifies one loaded module: its path and its content address.
type Ref struct {
	Path   string `json:"path"`   // canonical module path
	Digest string `json:"digest"` // "sha256:<hex>"; see Digest
}

// Dep is one direct dependency of a program or module as its digest sees it:
// a load() spec exactly as written in the source, and the digest of the
// module that spec resolved to.
type Dep struct {
	Spec   string `json:"spec"`   // the load() string, unresolved
	Digest string `json:"digest"` // the loaded module's Ref.Digest
}

// Digest is the content address of a program or module: a SHA-256, under
// the domain tag "craggy-module-v2", over src and the (spec, digest) pair of
// each direct dependency, sorted by spec, every string length-prefixed.
//
// Pairing each spec with what it loaded makes the digest say which module
// stands behind which load(): swapping the contents of two dependencies
// changes it. Transitive dependencies are covered through their parents'
// digests. Paths are not covered (a spec is part of src already), so
// identical source with identical dependencies has one digest wherever it
// lives, and a file has the same digest as a program and as a module.
func Digest(src []byte, deps []Dep) string {
	ds := append([]Dep(nil), deps...)
	sort.Slice(ds, func(i, j int) bool {
		if ds[i].Spec != ds[j].Spec {
			return ds[i].Spec < ds[j].Spec
		}
		return ds[i].Digest < ds[j].Digest
	})
	h := sha256.New()
	var n [8]byte
	write := func(s string) {
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		h.Write(n[:])
		h.Write([]byte(s))
	}
	write("craggy-module-v2")
	write(string(src))
	binary.BigEndian.PutUint64(n[:], uint64(len(ds)))
	h.Write(n[:])
	for _, d := range ds {
		write(d.Spec)
		write(d.Digest)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// Resolve turns a load() spec into a canonical module path. from is the
// loading file's path, against which "./" and "../" specs resolve; "//x" and
// bare "x" specs are relative to the root. Specs must name a ".star" file
// and must not escape the root.
func Resolve(from, spec string) (string, error) {
	if spec == "" {
		return "", fmt.Errorf("empty module path")
	}
	if strings.ContainsAny(spec, "\\\x00") {
		return "", fmt.Errorf("bad module path %q", spec)
	}
	var p string
	switch {
	case strings.HasPrefix(spec, "//"):
		p = path.Clean(spec[2:])
	case strings.HasPrefix(spec, "/"):
		return "", fmt.Errorf("module path %q is absolute: use //%s for a root path", spec, strings.TrimLeft(spec, "/"))
	case strings.HasPrefix(spec, "./"), strings.HasPrefix(spec, "../"):
		p = path.Join(path.Dir(path.Clean(from)), spec)
	default:
		p = path.Clean(spec)
	}
	if escapes(p) {
		return "", fmt.Errorf("module path %q escapes the module root", spec)
	}
	if !strings.HasSuffix(p, ".star") || path.Base(p) == ".star" {
		return "", fmt.Errorf("module path %q must name a .star file", spec)
	}
	return p, nil
}

func escapes(p string) bool {
	p = path.Clean(p)
	return p == ".." || strings.HasPrefix(p, "../") || p == "." || p == "/" || strings.HasPrefix(p, "/")
}

// Error is a module that failed to load: unreadable, not valid in the
// dialect, or failing (or exceeding its limits) while it ran.
type Error struct {
	Path string          // the failing module
	Pos  syntax.Position // innermost position in that module; may be invalid
	Msg  string          // what went wrong
	err  error
}

// Error formats the error as "path:line:col: message".
func (e *Error) Error() string {
	if e.Pos.IsValid() {
		return fmt.Sprintf("%s:%d:%d: %s", e.Path, e.Pos.Line, e.Pos.Col, e.Msg)
	}
	return e.Path + ": " + e.Msg
}

// Unwrap returns the underlying cause.
func (e *Error) Unwrap() error { return e.err }

// CycleError is a load cycle. Chain starts and ends with the same path.
type CycleError struct {
	Chain []string // the modules in the cycle, in load order
}

// Error names the chain, as in "load cycle: a.star -> b.star -> a.star".
func (e *CycleError) Error() string { return "load cycle: " + strings.Join(e.Chain, " -> ") }

// resolveMessage joins resolve errors into one message for an error whose
// position is the first one's: the first message bare, each later one
// prefixed with its own "line:col: ". A single error is just its message.
func resolveMessage(errs resolve.ErrorList) string {
	msgs := make([]string, len(errs))
	for i, r := range errs {
		if i == 0 {
			msgs[i] = r.Msg
			continue
		}
		msgs[i] = fmt.Sprintf("%d:%d: %s", r.Pos.Line, r.Pos.Col, r.Msg)
	}
	return strings.Join(msgs, "; ")
}

// ErrNoSource is the error load() fails with on a thread whose host gave no
// module source.
var ErrNoSource = errors.New("no module source: this host does not support load()")

// Options configure a Loader.
type Options struct {
	// Predeclared are the names every module sees besides the Starlark
	// universe. Hosts decide what modules may touch; scripts' own globals
	// (such as input) are not visible to modules.
	Predeclared starlark.StringDict
	// MaxSteps bounds the execution steps of each module's top level; 0
	// means unbounded.
	MaxSteps uint64
}

// Loader loads modules from one Source, executing each at most once. It is
// safe for concurrent use.
type Loader struct {
	src  Source
	opts Options
	key  string

	mu      sync.Mutex
	entries map[string]*entry
	waits   map[string]string // module path -> path it is waiting on
}

type entry struct {
	path    string
	done    chan struct{}
	globals starlark.StringDict
	ref     Ref
	deps    []*entry // direct dependencies
	err     error
	retry   bool // execution was cut short by its context; not cached
}

// NewLoader returns a Loader reading from src.
func NewLoader(src Source, opts Options) *Loader {
	l := &Loader{src: src, opts: opts, entries: map[string]*entry{}, waits: map[string]string{}}
	l.key = fmt.Sprintf("craggy-script/modules.Loader:%p", l)
	return l
}

// record is what programs on one thread loaded directly, keyed by load()
// spec as written.
type record struct{ loaded map[string]*entry }

func (l *Loader) record(thread *starlark.Thread) *record {
	if r, ok := thread.Local(l.key).(*record); ok {
		return r
	}
	r := &record{loaded: map[string]*entry{}}
	thread.SetLocal(l.key, r)
	return r
}

// Bind installs thread.Load so that load() statements of programs on thread
// load through l. from is the loading file's path (for "./" and "../"
// specs). Loads run under ctx.
func (l *Loader) Bind(ctx context.Context, thread *starlark.Thread, from string) {
	l.bind(ctx, thread, from, "")
}

func (l *Loader) bind(ctx context.Context, thread *starlark.Thread, from, waiter string) {
	l.record(thread)
	thread.Load = func(th *starlark.Thread, spec string) (starlark.StringDict, error) {
		p, err := Resolve(from, spec)
		if err != nil {
			return nil, err
		}
		e, err := l.load(ctx, th, waiter, p)
		if err != nil {
			return nil, err
		}
		l.record(th).loaded[spec] = e
		return e.globals, nil
	}
}

// Deps returns the direct dependencies of programs on thread: one Dep per
// distinct load() spec, sorted by spec. Digest(source, l.Deps(thread)) is the
// program's digest.
func (l *Loader) Deps(thread *starlark.Thread) []Dep {
	r, ok := thread.Local(l.key).(*record)
	if !ok {
		return nil
	}
	return r.deps()
}

func (r *record) deps() []Dep {
	out := make([]Dep, 0, len(r.loaded))
	for spec, e := range r.loaded {
		out = append(out, Dep{Spec: spec, Digest: e.ref.Digest})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Spec < out[j].Spec })
	return out
}

// Refs returns every module loaded, directly or transitively, by programs on
// thread, sorted by path. It is the provenance list; the digest covers only
// direct dependencies (see Deps).
func (l *Loader) Refs(thread *starlark.Thread) []Ref {
	r, ok := thread.Local(l.key).(*record)
	if !ok {
		return nil
	}
	seen := map[string]Ref{}
	var walk func(e *entry)
	walk = func(e *entry) {
		if _, ok := seen[e.path]; ok {
			return
		}
		seen[e.path] = e.ref
		for _, d := range e.deps {
			walk(d)
		}
	}
	for _, e := range r.loaded {
		walk(e)
	}
	return sortedRefs(seen)
}

func sortedRefs(m map[string]Ref) []Ref {
	out := make([]Ref, 0, len(m))
	for _, r := range m {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Module loads the module at the canonical path p and returns its frozen
// globals and Ref.
func (l *Loader) Module(ctx context.Context, p string) (starlark.StringDict, Ref, error) {
	canon, err := Resolve("", "//"+p)
	if err != nil {
		return nil, Ref{}, err
	}
	if canon != p {
		return nil, Ref{}, fmt.Errorf("module path %q is not canonical (want %q)", p, canon)
	}
	e, err := l.load(ctx, nil, "", p)
	if err != nil {
		return nil, Ref{}, err
	}
	return e.globals, e.ref, nil
}

// load returns the executed module at p. waiter is the module whose
// execution is asking ("" for a top-level program), for cycle detection.
func (l *Loader) load(ctx context.Context, parent *starlark.Thread, waiter, p string) (*entry, error) {
	for {
		l.mu.Lock()
		e, ok := l.entries[p]
		if !ok {
			e = &entry{path: p, done: make(chan struct{})}
			l.entries[p] = e
			if waiter != "" {
				l.waits[waiter] = p
			}
			l.mu.Unlock()
			l.exec(ctx, parent, e)
			l.mu.Lock()
			if waiter != "" {
				delete(l.waits, waiter)
			}
			if e.err != nil && ctx.Err() != nil {
				e.retry = true
				delete(l.entries, p)
			}
			close(e.done)
			l.mu.Unlock()
			return e, e.err
		}
		select {
		case <-e.done:
			l.mu.Unlock()
			if e.retry {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				continue
			}
			return e, e.err
		default:
		}
		// In progress elsewhere: wait, unless waiting would close a cycle.
		if waiter != "" {
			if chain := l.chain(p, waiter); chain != nil {
				l.mu.Unlock()
				return nil, &CycleError{Chain: append(chain, p)}
			}
			l.waits[waiter] = p
		}
		l.mu.Unlock()
		var err error
		select {
		case <-e.done:
		case <-ctx.Done():
			err = ctx.Err()
		}
		if waiter != "" {
			l.mu.Lock()
			delete(l.waits, waiter)
			l.mu.Unlock()
		}
		if err != nil {
			return nil, err
		}
		if e.retry {
			continue
		}
		return e, e.err
	}
}

// chain returns the wait chain from -> ... -> to, or nil when from is not
// (transitively) waiting on to. l.mu must be held.
func (l *Loader) chain(from, to string) []string {
	out := []string{from}
	for cur := from; cur != to; {
		next, ok := l.waits[cur]
		if !ok || len(out) > len(l.waits)+1 {
			return nil
		}
		out = append(out, next)
		cur = next
	}
	return out
}

func (l *Loader) exec(ctx context.Context, parent *starlark.Thread, e *entry) {
	fail := func(pos syntax.Position, msg string, err error) {
		e.err = &Error{Path: e.path, Pos: pos, Msg: msg, err: err}
	}
	src, err := l.src.Read(ctx, e.path)
	if err != nil {
		fail(syntax.Position{}, err.Error(), err)
		return
	}
	thread := &starlark.Thread{Name: "module:" + e.path, Print: func(*starlark.Thread, string) {}}
	if parent != nil && parent.Print != nil {
		thread.Print = parent.Print
	}
	if l.opts.MaxSteps > 0 {
		thread.SetMaxExecutionSteps(l.opts.MaxSteps)
	}
	l.bind(ctx, thread, e.path, e.path)
	if err := ctx.Err(); err != nil {
		thread.Cancel("context done: " + err.Error())
	}
	stop := context.AfterFunc(ctx, func() { thread.Cancel("context done: " + ctx.Err().Error()) })
	defer stop()

	prog, err := dialect.Compile(e.path, src, l.opts.Predeclared.Has)
	if err != nil {
		pos, msg := describe(e.path, err)
		fail(pos, msg, err)
		return
	}
	globals, err := prog.Init(thread, l.opts.Predeclared)
	globals.Freeze()
	if err != nil {
		pos, msg := describe(e.path, err)
		fail(pos, msg, err)
		return
	}
	rec := l.record(thread)
	byPath := map[string]*entry{}
	for _, d := range rec.loaded {
		byPath[d.path] = d
	}
	deps := make([]*entry, 0, len(byPath))
	for _, d := range byPath {
		deps = append(deps, d)
	}
	sort.Slice(deps, func(i, j int) bool { return deps[i].path < deps[j].path })
	e.globals, e.deps = globals, deps
	e.ref = Ref{Path: e.path, Digest: Digest(src, rec.deps())}
}

// describe extracts the innermost position in file and a message from a
// compile or execution error.
func describe(file string, err error) (syntax.Position, string) {
	var evalErr *starlark.EvalError
	var synErr syntax.Error
	switch {
	case errors.As(err, &evalErr):
		var pos syntax.Position
		for _, fr := range evalErr.CallStack {
			if fr.Pos.Filename() == file {
				pos = fr.Pos
			}
		}
		return pos, evalErr.Msg
	case errors.As(err, &synErr):
		return synErr.Pos, synErr.Msg
	}
	var resErrs resolve.ErrorList
	if errors.As(err, &resErrs) && len(resErrs) > 0 {
		return resErrs[0].Pos, resolveMessage(resErrs)
	}
	return syntax.Position{}, err.Error()
}
