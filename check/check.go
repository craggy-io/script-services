// Package check validates a script without running it. It parses and
// resolves the source in the Craggy dialect against what the host will
// predeclare, then checks every reference it can see statically:
//
//   - a dotted path from a callable namespace must name a callable or a
//     namespace (unknown names come with a nearest-name suggestion);
//   - a literal ref passed to a ref function (such as call("x.y@2") or
//     jobs.start("x.y")) must resolve;
//   - keyword arguments of a direct call must fit the callable's input
//     schema: no unknown names when the schema is closed, no missing
//     required ones (skipped when the call passes a positional dict, *args
//     or **kwargs);
//   - with a module source, load() targets must exist and parse, and the
//     names loaded from them must be defined there.
//
// Check knows nothing about what callables are; hosts describe them as
// Callable values (a capability registry passes its capabilities, with the
// registry entry in Value) and name them in messages with Env.Noun ("unknown
// callable x.y" by default; a registry passes "capability").
//
// Every diagnostic Check emits today is an error; Diagnostic.Severity is
// there so a later check can add warnings without changing the type.
package check

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"go.starlark.net/resolve"
	"go.starlark.net/syntax"

	"github.com/craggy-io/script-services/dialect"
	"github.com/craggy-io/script-services/modules"
	"github.com/craggy-io/script-services/script"
)

// Diagnostic is one problem Check found.
type Diagnostic struct {
	Pos script.Position `json:"pos"` // where the problem is
	// Severity is always "error" today. The field is kept so warnings can
	// be added later; callers should treat any other value as non-fatal.
	Severity string `json:"severity"`
	Message  string `json:"message"` // what is wrong, with a suggestion when one is close
}

// Callable is something a script can call by dotted name, such as
// "storage.artifact.get". Input is its input schema; nil means it takes no
// input. Value is the host's own payload, returned untouched in
// Analysis.Uses.
type Callable struct {
	Name  string             // dotted name, as scripts spell it
	Input *jsonschema.Schema // input schema; nil means no input
	Value any                // host payload, returned in Analysis.Uses
}

// Env describes what the host will predeclare when the script runs.
type Env struct {
	// Callables become nested namespaces keyed by their first name segment.
	Callables []Callable
	// Predeclared are other host names (input is always predeclared).
	Predeclared []string
	// RefFuncs are functions taking a ref string first, keyed by dotted
	// path ("call", "jobs.start"). For a call with a literal first
	// argument, the function resolves the ref; false means unknown.
	RefFuncs map[string]func(ref string) (Callable, bool)
	// Modules, when set, is where load() targets are looked up.
	Modules modules.Source
	// Noun names a callable in messages ("unknown <Noun> x.y", "x is a
	// namespace, not a <Noun>"). Empty means "callable"; a capability
	// registry passes "capability".
	Noun string
}

// Analysis is what Analyze found in a script without running it.
type Analysis struct {
	// Diagnostics are Check's findings, in source order.
	Diagnostics []Diagnostic
	// Uses is every callable a direct call or a literal ref resolves to,
	// once each by Name, in source order.
	Uses []Callable
	// Loads are the canonical paths of the script's load() targets, once
	// each, in source order.
	Loads []string
}

// Check returns Analyze's diagnostics. An empty result means the script
// compiles and every reference Check can see resolves; it says nothing
// about runtime values.
func Check(env Env, filename string, src []byte) []Diagnostic {
	return Analyze(env, filename, src).Diagnostics
}

// refFunc marks a RefFuncs entry in the name tree.
type refFunc struct {
	path    string
	resolve func(string) (Callable, bool)
}

// Analyze checks src (see the package doc) and reports what it calls and
// loads. filename defaults to "script.star" and is the base for relative
// load() paths.
func Analyze(env Env, filename string, src []byte) Analysis {
	if filename == "" {
		filename = "script.star"
	}
	noun := env.Noun
	if noun == "" {
		noun = "callable"
	}
	c := &checker{tree: map[string]any{}, filename: filename, noun: noun}
	for _, cl := range env.Callables {
		c.insert(cl.Name, cl)
		c.names = append(c.names, cl.Name)
	}
	refPaths := make([]string, 0, len(env.RefFuncs))
	for k := range env.RefFuncs {
		refPaths = append(refPaths, k)
	}
	sort.Strings(refPaths)
	for _, k := range refPaths {
		c.insert(k, &refFunc{path: k, resolve: env.RefFuncs[k]})
	}
	predeclared := map[string]bool{"input": true}
	for k := range c.tree {
		predeclared[k] = true
	}
	for _, k := range env.Predeclared {
		predeclared[k] = true
	}
	f, err := dialect.Parse(filename, src)
	if err != nil {
		if se, ok := err.(syntax.Error); ok {
			return Analysis{Diagnostics: []Diagnostic{{Pos: script.PositionOf(se.Pos), Severity: "error", Message: se.Msg}}}
		}
		return Analysis{Diagnostics: []Diagnostic{{Severity: "error", Message: err.Error()}}}
	}
	if err := dialect.Resolve(f, func(n string) bool { return predeclared[n] }); err != nil {
		if list, ok := err.(resolve.ErrorList); ok {
			for _, e := range list {
				c.add(e.Pos, "error", e.Msg)
			}
		} else {
			c.add(syntax.Position{}, "error", err.Error())
		}
	}
	for _, stmt := range f.Stmts {
		if ld, ok := stmt.(*syntax.LoadStmt); ok {
			c.checkLoad(env.Modules, ld)
		}
	}
	syntax.Walk(f, c.visit)
	sort.SliceStable(c.diags, func(i, j int) bool {
		a, b := c.diags[i].Pos, c.diags[j].Pos
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Col < b.Col
	})
	return Analysis{Diagnostics: c.diags, Uses: c.uses, Loads: c.loads}
}

type checker struct {
	tree     map[string]any // inner nodes map[string]any; leaves Callable or *refFunc
	filename string
	noun     string // what callables are called in messages
	names    []string
	diags    []Diagnostic
	uses     []Callable
	used     map[string]bool
	loads    []string
}

// insert places a leaf at a dotted path. Ref functions take precedence over
// a callable of the same name; namespaces and leaves never replace each
// other's children.
func (c *checker) insert(name string, leaf any) {
	parts := strings.Split(name, ".")
	node := c.tree
	for _, p := range parts[:len(parts)-1] {
		next, ok := node[p].(map[string]any)
		if !ok {
			if _, isLeaf := node[p]; isLeaf {
				return
			}
			next = map[string]any{}
			node[p] = next
		}
		node = next
	}
	last := parts[len(parts)-1]
	if existing, ok := node[last]; ok {
		if _, isNS := existing.(map[string]any); isNS {
			return
		}
		if _, isRef := leaf.(*refFunc); !isRef {
			return
		}
	}
	node[last] = leaf
}

func (c *checker) use(cl Callable) {
	if c.used[cl.Name] {
		return
	}
	if c.used == nil {
		c.used = map[string]bool{}
	}
	c.used[cl.Name] = true
	c.uses = append(c.uses, cl)
}

func (c *checker) add(pos syntax.Position, severity, msg string) {
	c.diags = append(c.diags, Diagnostic{Pos: script.PositionOf(pos), Severity: severity, Message: msg})
}

func (c *checker) checkLoad(src modules.Source, ld *syntax.LoadStmt) {
	spec, _ := ld.Module.Value.(string)
	p, err := modules.Resolve(c.filename, spec)
	if err != nil {
		c.add(ld.Module.TokenPos, "error", fmt.Sprintf("load %q: %v", spec, err))
		return
	}
	seen := false
	for _, l := range c.loads {
		seen = seen || l == p
	}
	if !seen {
		c.loads = append(c.loads, p)
	}
	if src == nil {
		return
	}
	data, err := src.Read(context.Background(), p)
	if err != nil {
		c.add(ld.Module.TokenPos, "error", fmt.Sprintf("load %q: cannot read %s: %v", spec, p, err))
		return
	}
	mf, err := dialect.Parse(p, data)
	if err != nil {
		msg := err.Error()
		if se, ok := err.(syntax.Error); ok {
			msg = fmt.Sprintf("%s:%d:%d: %s", p, se.Pos.Line, se.Pos.Col, se.Msg)
		}
		c.add(ld.Module.TokenPos, "error", fmt.Sprintf("load %q: %s does not parse: %s", spec, p, msg))
		return
	}
	// Resolve with everything predeclared: only the module's own globals
	// matter here, not what its host provides.
	_ = dialect.Resolve(mf, func(string) bool { return true })
	defined := map[string]bool{}
	var names []string
	if m, ok := mf.Module.(*resolve.Module); ok {
		for _, b := range m.Globals {
			defined[b.First.Name] = true
			names = append(names, b.First.Name)
		}
	}
	for _, from := range ld.From {
		if defined[from.Name] || strings.HasPrefix(from.Name, "_") {
			continue // private names are already a resolve error
		}
		msg := fmt.Sprintf("load %q: %s does not define %s", spec, p, from.Name)
		if s := nearest(from.Name, names); s != "" {
			msg += fmt.Sprintf(" (did you mean %s?)", s)
		}
		c.add(from.NamePos, "error", msg)
	}
}

func (c *checker) visit(n syntax.Node) bool {
	switch t := n.(type) {
	case *syntax.LoadStmt:
		return false
	case *syntax.CallExpr:
		c.checkCall(t)
		for _, a := range t.Args {
			syntax.Walk(a, c.visit)
		}
		if _, _, ok := c.rootedPath(t.Fn); !ok {
			syntax.Walk(t.Fn, c.visit)
		}
		return false
	case *syntax.DotExpr:
		if _, _, ok := c.rootedPath(t); ok {
			c.resolvePath(t)
			return false
		}
	}
	return true
}

// rootedPath returns the dotted path of e when it is an identifier, or a
// chain of attribute selections, rooted at a predeclared tree root.
func (c *checker) rootedPath(e syntax.Expr) (*syntax.Ident, []string, bool) {
	var parts []string
	for {
		switch t := e.(type) {
		case *syntax.DotExpr:
			parts = append([]string{t.Name.Name}, parts...)
			e = t.X
		case *syntax.Ident:
			b, _ := t.Binding.(*resolve.Binding)
			if b == nil || b.Scope != resolve.Predeclared {
				return nil, nil, false
			}
			if _, ok := c.tree[t.Name]; !ok {
				return nil, nil, false
			}
			return t, parts, true
		default:
			return nil, nil, false
		}
	}
}

// resolvePath reports an unknown path and returns what it names: a
// Callable, a namespace (map), a *refFunc, or nil.
func (c *checker) resolvePath(e syntax.Expr) any {
	root, parts, ok := c.rootedPath(e)
	if !ok {
		return nil
	}
	node := c.tree[root.Name]
	path := root.Name
	for _, p := range parts {
		m, isNS := node.(map[string]any)
		if !isNS {
			kind := c.noun
			if _, isRef := node.(*refFunc); isRef {
				kind = "function"
			}
			c.add(root.NamePos, "error", fmt.Sprintf("%s is a %s, not a namespace: it has no attribute %q", path, kind, p))
			return nil
		}
		path += "." + p
		next, ok := m[p]
		if !ok {
			msg := "unknown " + c.noun + " " + path
			if s := nearest(path, c.names); s != "" {
				msg += fmt.Sprintf(" (did you mean %s?)", s)
			}
			c.add(root.NamePos, "error", msg)
			return nil
		}
		node = next
	}
	return node
}

func (c *checker) checkCall(call *syntax.CallExpr) {
	target := c.resolvePath(call.Fn)
	if target == nil {
		return
	}
	pos, _ := call.Span()
	switch t := target.(type) {
	case Callable:
		c.use(t)
		c.checkArgs(pos, t, call.Args)
	case map[string]any:
		c.add(pos, "error", fmt.Sprintf("%s is a namespace, not a %s", exprPath(call.Fn), c.noun))
	case *refFunc:
		if len(call.Args) == 0 {
			return
		}
		lit, ok := call.Args[0].(*syntax.Literal)
		if !ok || lit.Token != syntax.STRING {
			return
		}
		ref, _ := lit.Value.(string)
		var cl Callable
		found := false
		if t.resolve != nil {
			cl, found = t.resolve(ref)
		}
		if !found {
			msg := fmt.Sprintf("unknown %s %q", c.noun, ref)
			if s := nearest(stripVersion(ref), c.names); s != "" {
				msg += fmt.Sprintf(" (did you mean %s?)", s)
			}
			c.add(pos, "error", msg)
			return
		}
		c.use(cl)
		c.checkArgs(pos, cl, call.Args[1:])
	}
}

// stripVersion drops an "@N" version suffix from a ref.
func stripVersion(ref string) string {
	i := strings.LastIndexByte(ref, '@')
	if i < 0 || i == len(ref)-1 {
		return ref
	}
	for _, r := range ref[i+1:] {
		if r < '0' || r > '9' {
			return ref
		}
	}
	return ref[:i]
}

// checkArgs checks keyword arguments against the callable's input schema.
func (c *checker) checkArgs(pos syntax.Position, cl Callable, args []syntax.Expr) {
	kw := map[string]bool{}
	for _, a := range args {
		switch t := a.(type) {
		case *syntax.BinaryExpr:
			if t.Op == syntax.EQ {
				if id, ok := t.X.(*syntax.Ident); ok {
					kw[id.Name] = true
					continue
				}
			}
			return
		default:
			return // positional dict, *args or **kwargs: not statically known
		}
	}
	in := cl.Input
	if in == nil {
		if len(kw) > 0 {
			c.add(pos, "error", fmt.Sprintf("%s takes no input", cl.Name))
		}
		return
	}
	closed := false
	if in.AdditionalProperties != nil {
		b, _ := json.Marshal(in.AdditionalProperties)
		closed = string(b) == "false"
	}
	if closed {
		known := make([]string, 0, len(in.Properties))
		for k := range in.Properties {
			known = append(known, k)
		}
		sort.Strings(known)
		var unknown []string
		for k := range kw {
			if _, ok := in.Properties[k]; !ok {
				unknown = append(unknown, k)
			}
		}
		sort.Strings(unknown)
		for _, k := range unknown {
			msg := fmt.Sprintf("%s has no input %q (inputs: %s)", cl.Name, k, strings.Join(known, ", "))
			if s := nearest(k, known); s != "" {
				msg = fmt.Sprintf("%s has no input %q (did you mean %s?)", cl.Name, k, s)
			}
			c.add(pos, "error", msg)
		}
	}
	for _, r := range in.Required {
		if !kw[r] {
			c.add(pos, "error", fmt.Sprintf("%s requires input %q", cl.Name, r))
		}
	}
}

func exprPath(e syntax.Expr) string {
	switch t := e.(type) {
	case *syntax.DotExpr:
		return exprPath(t.X) + "." + t.Name.Name
	case *syntax.Ident:
		return t.Name
	}
	return "?"
}

// nearest returns the candidate closest to s by edit distance, or "" when
// none is close enough to be a plausible typo.
func nearest(s string, candidates []string) string {
	best, bestD := "", -1
	for _, c := range candidates {
		d := editDistance(s, c)
		if bestD < 0 || d < bestD {
			best, bestD = c, d
		}
	}
	limit := len(s) / 3
	if limit < 2 {
		limit = 2
	}
	if bestD < 0 || bestD > limit {
		return ""
	}
	return best
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
