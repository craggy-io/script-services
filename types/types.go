// Package types turns Go-syntax type declarations into JSON Schemas, so a
// script's inputs and outputs can be declared the way Go programmers already
// write them:
//
//	// DecompileIn is what apk.decompile takes.
//	type DecompileIn struct {
//		Artifact string `json:"artifact" jsonschema:"artifact id"`
//		Depth    *int   `json:"depth,omitempty"` // how deep to go
//		Level    Level  `json:"level"`
//	}
//
//	// Level is a decompiler effort level.
//	// enum: fast, full
//	type Level string
//
// The source is parsed with go/parser and never compiled or type-checked
// against other packages. The shapes and required sets match what
// jsonschema.For (v0.4.3) derives from the same Go types (the deriver Go
// handlers in capability registries go through), so a type moved between a
// Go handler and a declaration file keeps its schema:
//
//   - string, bool, every int and uint kind (integer, with the same range
//     bounds as jsonschema.For), float32/float64 (number);
//   - []T is an array that also admits null, [N]T an array of exactly N items,
//     map[string]T an object whose additionalProperties is T's schema;
//   - structs are closed objects (additionalProperties false) listing
//     properties in field order; a field is required unless tagged
//     omitempty/omitzero (pointers included: required but nullable);
//     json:"-" and unexported fields are skipped; embedded structs promote
//     their fields, and promoted fields sharing a JSON name follow
//     encoding/json (shallowest wins, then the one tagged field, else all
//     are dropped);
//   - *T is T's schema that also admits null (an enum gains null too);
//   - any and interface{} are unconstrained.
//
// Deliberate deviations from jsonschema.For: time.Time adds format
// date-time; time.Duration (an integer of nanoseconds) adds a description;
// []byte is a base64 string and json.RawMessage is unconstrained, matching
// encoding/json where For renders an integer array; an "enum:" doc line adds
// an enum. Other declared types are inlined, named non-struct types
// included.
//
// Descriptions come from a field's `jsonschema:"..."` tag, else its doc or
// line comment; a declared type's description is its doc comment. A line
// "enum: a, b, c" in a named type's doc comment restricts it to those values
// (integers are decimal).
// Recursive types and unsupported types (channels, functions, generics,
// non-string map keys, other packages' types) are errors with positions.
//
// In a bundle, a declaration file is a contribution of kind craggy.types@1
// (Declarations), compiled by Compiler into its *Package.
package types

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	gotypes "go/types"
	"math"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

// Package is a parsed Go file of type declarations.
type Package struct {
	// Name is the Go package name from the package clause.
	Name    string
	fset    *token.FileSet
	imports map[string]string // local name -> import path
	decls   map[string]*decl
}

type decl struct {
	spec *ast.TypeSpec
	desc string
	enum []string
	pos  token.Pos // of the enum line's comment group, for enum errors
}

// Error is a problem in a declaration file, with the position it refers to.
type Error struct {
	Pos token.Position // where in the declaration file
	Msg string         // what is wrong
}

// Error formats the error as "file:line:col: message".
func (e *Error) Error() string {
	if e.Pos.IsValid() {
		return e.Pos.String() + ": " + e.Msg
	}
	return e.Msg
}

// Parse parses src, a Go source file with a package clause, and collects its
// type declarations. Other declarations are ignored. Problems inside a type
// surface from Schema, not from Parse.
func Parse(filename string, src []byte) (*Package, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	p := &Package{Name: f.Name.Name, fset: fset, imports: map[string]string{}, decls: map[string]*decl{}}
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return nil, p.errorf(imp.Path.Pos(), "bad import path %s", imp.Path.Value)
		}
		name := path[strings.LastIndex(path, "/")+1:]
		if imp.Name != nil {
			name = imp.Name.Name
		}
		p.imports[name] = path
	}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, s := range gd.Specs {
			ts := s.(*ast.TypeSpec)
			doc := ts.Doc
			if doc == nil && len(gd.Specs) == 1 {
				doc = gd.Doc
			}
			dd := &decl{spec: ts}
			if doc != nil {
				dd.desc, dd.enum = splitDoc(doc.Text())
				dd.pos = doc.Pos()
			}
			if _, dup := p.decls[ts.Name.Name]; dup {
				return nil, p.errorf(ts.Name.Pos(), "type %s is declared twice", ts.Name.Name)
			}
			p.decls[ts.Name.Name] = dd
		}
	}
	return p, nil
}

var enumLine = regexp.MustCompile(`^\s*enum:\s*(.*)$`)

// splitDoc separates a doc comment's "enum: a, b" line from its prose.
func splitDoc(text string) (string, []string) {
	var keep []string
	var enum []string
	for _, line := range strings.Split(text, "\n") {
		if m := enumLine.FindStringSubmatch(line); m != nil {
			for _, v := range splitEnum(m[1]) {
				if v = strings.TrimSpace(v); v != "" {
					enum = append(enum, v)
				}
			}
			continue
		}
		keep = append(keep, line)
	}
	return strings.TrimSpace(strings.Join(keep, "\n")), enum
}

// splitEnum splits an enum list on commas, ignoring commas inside quoted
// ("..." or `...`) values.
func splitEnum(s string) []string {
	var out []string
	var cur strings.Builder
	var quote byte // 0, '"' or '`'
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case escaped:
			escaped = false
		case quote == '"' && c == '\\':
			escaped = true
		case quote != 0 && c == quote:
			quote = 0
		case quote == 0 && (c == '"' || c == '`'):
			quote = c
		case quote == 0 && c == ',':
			out = append(out, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteByte(c)
	}
	return append(out, cur.String())
}

// Names returns the declared type names, sorted.
func (p *Package) Names() []string {
	out := make([]string, 0, len(p.decls))
	for k := range p.decls {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Has reports whether the package declares a type called name.
func (p *Package) Has(name string) bool {
	_, ok := p.decls[name]
	return ok
}

// Schema derives the JSON Schema of the declared type name. Each call
// returns a fresh schema the caller may modify.
func (p *Package) Schema(name string) (*jsonschema.Schema, error) {
	d, ok := p.decls[name]
	if !ok {
		return nil, fmt.Errorf("types: unknown type %q", name)
	}
	b := &builder{p: p}
	return b.named(d, d.spec.Name.Pos())
}

func (p *Package) errorf(pos token.Pos, format string, args ...any) error {
	return &Error{Pos: p.fset.Position(pos), Msg: fmt.Sprintf(format, args...)}
}

type builder struct {
	p     *Package
	stack []string // declared types being expanded
}

func (b *builder) named(d *decl, at token.Pos) (*jsonschema.Schema, error) {
	name := d.spec.Name.Name
	for i, n := range b.stack {
		if n == name {
			chain := append(append([]string(nil), b.stack[i:]...), name)
			return nil, b.p.errorf(at, "recursive type %s (%s)", name, strings.Join(chain, " -> "))
		}
	}
	if d.spec.TypeParams != nil {
		return nil, b.p.errorf(d.spec.Name.Pos(), "generic type %s is unsupported", name)
	}
	b.stack = append(b.stack, name)
	defer func() { b.stack = b.stack[:len(b.stack)-1] }()
	s, err := b.expr(d.spec.Type)
	if err != nil {
		return nil, err
	}
	if d.desc != "" {
		s.Description = d.desc
	}
	if len(d.enum) > 0 {
		if s.Enum, err = enumValues(s, d.enum); err != nil {
			return nil, b.p.errorf(d.pos, "type %s: %v", name, err)
		}
	}
	return s, nil
}

func enumValues(s *jsonschema.Schema, vals []string) ([]any, error) {
	out := make([]any, 0, len(vals))
	for _, v := range vals {
		switch s.Type {
		case "string":
			if uq, err := strconv.Unquote(v); err == nil {
				v = uq
			}
			out = append(out, v)
		case "integer":
			n, err := enumInt(s, v)
			if err != nil {
				return nil, err
			}
			out = append(out, n)
		case "number":
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return nil, fmt.Errorf("enum value %q is not a number", v)
			}
			if math.IsNaN(f) || math.IsInf(f, 0) {
				return nil, fmt.Errorf("enum value %q is not a finite number", v)
			}
			out = append(out, f)
		case "boolean":
			bv, err := strconv.ParseBool(v)
			if err != nil {
				return nil, fmt.Errorf("enum value %q is not a bool", v)
			}
			out = append(out, bv)
		default:
			return nil, fmt.Errorf("enum needs a string, integer, number or bool type")
		}
	}
	return out, nil
}

// enumInt parses one integer enum value against the kind's range as the
// schema records it (Minimum/Maximum): unsigned kinds (Minimum 0) parse as
// uint64, so their full range is accepted, and bounded kinds reject values
// they cannot hold.
func enumInt(s *jsonschema.Schema, v string) (any, error) {
	notInt := fmt.Errorf("enum value %q is not an integer", v)
	outOfRange := fmt.Errorf("enum value %q is out of range for the type", v)
	isRangeErr := func(err error) bool {
		var ne *strconv.NumError
		return errors.As(err, &ne) && ne.Err == strconv.ErrRange
	}
	if s.Minimum != nil && *s.Minimum == 0 { // unsigned kind
		n, err := strconv.ParseUint(v, 10, 64) // decimal: "010" is ten
		if err != nil {
			if isRangeErr(err) || strings.HasPrefix(v, "-") {
				return nil, outOfRange
			}
			return nil, notInt
		}
		if s.Maximum != nil && n > uint64(*s.Maximum) {
			return nil, outOfRange
		}
		return n, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		if isRangeErr(err) {
			return nil, outOfRange
		}
		return nil, notInt
	}
	if (s.Minimum != nil && n < int64(*s.Minimum)) || (s.Maximum != nil && n > int64(*s.Maximum)) {
		return nil, outOfRange
	}
	return n, nil
}

func f64(f float64) *float64 { return &f }

func intSchema(min, max float64, bounded, unsigned bool) *jsonschema.Schema {
	s := &jsonschema.Schema{Type: "integer"}
	if unsigned {
		s.Minimum = f64(0)
	}
	if bounded {
		s.Minimum, s.Maximum = f64(min), f64(max)
	}
	return s
}

// basic returns the schema of a predeclared Go type, or nil.
func basic(name string) *jsonschema.Schema {
	switch name {
	case "string":
		return &jsonschema.Schema{Type: "string"}
	case "bool":
		return &jsonschema.Schema{Type: "boolean"}
	case "int", "int64":
		return intSchema(0, 0, false, false)
	case "uint", "uint64", "uintptr":
		return intSchema(0, 0, false, true)
	case "int8":
		return intSchema(math.MinInt8, math.MaxInt8, true, false)
	case "uint8", "byte":
		return intSchema(0, math.MaxUint8, true, false)
	case "int16":
		return intSchema(math.MinInt16, math.MaxInt16, true, false)
	case "uint16":
		return intSchema(0, math.MaxUint16, true, false)
	case "int32", "rune":
		return intSchema(math.MinInt32, math.MaxInt32, true, false)
	case "uint32":
		return intSchema(0, math.MaxUint32, true, false)
	case "float32", "float64":
		return &jsonschema.Schema{Type: "number"}
	case "any":
		return &jsonschema.Schema{}
	}
	return nil
}

const base64Note = "base64-encoded bytes"

func (b *builder) isByte(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	if !ok {
		return false
	}
	if _, declared := b.p.decls[id.Name]; declared {
		return false
	}
	return id.Name == "byte" || id.Name == "uint8"
}

func (b *builder) expr(e ast.Expr) (*jsonschema.Schema, error) {
	switch t := e.(type) {
	case *ast.ParenExpr:
		return b.expr(t.X)
	case *ast.Ident:
		if d, ok := b.p.decls[t.Name]; ok {
			return b.named(d, t.Pos())
		}
		if s := basic(t.Name); s != nil {
			return s, nil
		}
		switch t.Name {
		case "complex64", "complex128", "error":
			return nil, b.p.errorf(t.Pos(), "unsupported type %s", t.Name)
		}
		return nil, b.p.errorf(t.Pos(), "unknown type %s", t.Name)
	case *ast.SelectorExpr:
		pkg, ok := t.X.(*ast.Ident)
		if !ok {
			return nil, b.p.errorf(t.Pos(), "unsupported type %s", gotypes.ExprString(t))
		}
		path, ok := b.p.imports[pkg.Name]
		if !ok {
			return nil, b.p.errorf(t.Pos(), "unknown package %s in %s (not imported)", pkg.Name, gotypes.ExprString(t))
		}
		switch path + "." + t.Sel.Name {
		case "time.Time":
			return &jsonschema.Schema{Type: "string", Format: "date-time"}, nil
		case "time.Duration":
			return &jsonschema.Schema{Type: "integer", Description: "duration in nanoseconds"}, nil
		case "encoding/json.RawMessage":
			return &jsonschema.Schema{}, nil
		}
		return nil, b.p.errorf(t.Pos(), "unsupported type %s", gotypes.ExprString(t))
	case *ast.StarExpr:
		s, err := b.expr(t.X)
		if err != nil {
			return nil, err
		}
		return allowNull(s), nil
	case *ast.ArrayType:
		if t.Len == nil && b.isByte(t.Elt) {
			return &jsonschema.Schema{Type: "string", ContentEncoding: "base64", Description: base64Note}, nil
		}
		items, err := b.expr(t.Elt)
		if err != nil {
			return nil, err
		}
		if t.Len == nil {
			return &jsonschema.Schema{Types: []string{"null", "array"}, Items: items}, nil
		}
		lit, ok := t.Len.(*ast.BasicLit)
		if !ok || lit.Kind != token.INT {
			return nil, b.p.errorf(t.Len.Pos(), "array length must be an integer literal")
		}
		n, err := strconv.ParseInt(lit.Value, 0, 32)
		if err != nil || n < 0 {
			return nil, b.p.errorf(t.Len.Pos(), "bad array length %s", lit.Value)
		}
		ln := int(n)
		return &jsonschema.Schema{Type: "array", Items: items, MinItems: &ln, MaxItems: &ln}, nil
	case *ast.MapType:
		key, err := b.expr(t.Key)
		if err != nil {
			return nil, err
		}
		if key.Type != "string" {
			return nil, b.p.errorf(t.Key.Pos(), "unsupported map key type %s: keys must be strings", gotypes.ExprString(t.Key))
		}
		val, err := b.expr(t.Value)
		if err != nil {
			return nil, err
		}
		return &jsonschema.Schema{Type: "object", AdditionalProperties: val}, nil
	case *ast.InterfaceType:
		if t.Methods != nil && len(t.Methods.List) > 0 {
			return nil, b.p.errorf(t.Pos(), "unsupported type %s: only empty interfaces are allowed", gotypes.ExprString(t))
		}
		return &jsonschema.Schema{}, nil
	case *ast.StructType:
		return b.structType(t)
	}
	return nil, b.p.errorf(e.Pos(), "unsupported type %s", gotypes.ExprString(e))
}

// allowNull mirrors jsonschema.For's pointer rule: a typed schema also
// admits null. An enum (from a named type's "enum:" line) gains null too, or
// it would still reject it.
func allowNull(s *jsonschema.Schema) *jsonschema.Schema {
	if s.Type != "" {
		s.Types = []string{"null", s.Type}
		s.Type = ""
	} else if len(s.Types) > 0 && s.Types[0] != "null" {
		s.Types = append([]string{"null"}, s.Types...)
	}
	if len(s.Enum) > 0 && !slices.Contains(s.Enum, any(nil)) {
		s.Enum = append(s.Enum, nil)
	}
	return s
}

type prop struct {
	name     string
	schema   *jsonschema.Schema
	required bool
	depth    int  // 0 for the struct's own fields, +1 per embedding
	tagged   bool // the JSON name came from a json tag
	pos      token.Pos
}

var disallowedTag = regexp.MustCompile("^[^ \t\n]*=")

func (b *builder) structType(st *ast.StructType) (*jsonschema.Schema, error) {
	props, err := b.fields(st, 0)
	if err != nil {
		return nil, err
	}
	s := &jsonschema.Schema{Type: "object", AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}}}
	// encoding/json's rule for fields sharing a JSON name: the shallowest
	// wins; among several at that depth, the one tagged field wins, and
	// with no single tagged field all of them are dropped. Two of the
	// struct's own fields sharing a name are reported instead: that is a
	// mistake in the declaration, not an embedding to resolve.
	byName := map[string][]int{}
	var names []string // first-appearance order, for deterministic errors
	for i, pr := range props {
		if _, ok := byName[pr.name]; !ok {
			names = append(names, pr.name)
		}
		byName[pr.name] = append(byName[pr.name], i)
	}
	var winners []int // indexes into props, which is in field-index order
	for _, name := range names {
		cands := byName[name]
		depth := props[cands[0]].depth
		for _, c := range cands {
			depth = min(depth, props[c].depth)
		}
		var top, tagged []int
		for _, c := range cands {
			if props[c].depth == depth {
				top = append(top, c)
				if props[c].tagged {
					tagged = append(tagged, c)
				}
			}
		}
		switch {
		case len(top) == 1:
			winners = append(winners, top[0])
		case depth == 0:
			pr := props[top[1]]
			return nil, b.p.errorf(pr.pos, "duplicate JSON field name %q", pr.name)
		case len(tagged) == 1:
			winners = append(winners, tagged[0])
		}
	}
	// Properties follow the winners' field order, as in encoding/json.
	slices.Sort(winners)
	for _, i := range winners {
		pr := &props[i]
		name := pr.name
		if s.Properties == nil {
			s.Properties = map[string]*jsonschema.Schema{}
		}
		s.Properties[name] = pr.schema
		s.PropertyOrder = append(s.PropertyOrder, name)
		if pr.required {
			s.Required = append(s.Required, name)
		}
	}
	return s, nil
}

func (b *builder) fields(st *ast.StructType, depth int) ([]prop, error) {
	var out []prop
	for _, f := range st.Fields.List {
		var tag reflect.StructTag
		if f.Tag != nil {
			raw, err := strconv.Unquote(f.Tag.Value)
			if err != nil {
				return nil, b.p.errorf(f.Tag.Pos(), "bad struct tag %s", f.Tag.Value)
			}
			tag = reflect.StructTag(raw)
		}
		jsonName, opts, omit := jsonInfo(tag)
		if omit {
			continue
		}
		if len(f.Names) == 0 && jsonName == "" && b.embedsStruct(f.Type) {
			// Embedded struct: promote its fields. A non-struct embed
			// (or one with a json name) is a regular field instead, as
			// in encoding/json.
			inner, err := b.embedded(f.Type, depth)
			if err != nil {
				return nil, err
			}
			out = append(out, inner...)
			continue
		}
		names := f.Names
		isEmbed := len(names) == 0
		if isEmbed {
			names = []*ast.Ident{{Name: embeddedName(f.Type), NamePos: f.Type.Pos()}}
		}
		for _, id := range names {
			// An embedded field of unexported type with an explicit
			// json name is visible under that name (encoding/json
			// emits it); other unexported fields are not.
			if !ast.IsExported(id.Name) && !(isEmbed && jsonName != "") {
				continue
			}
			fs, err := b.expr(f.Type)
			if err != nil {
				return nil, err
			}
			desc := ""
			if v, ok := tag.Lookup("jsonschema"); ok {
				if v == "" {
					return nil, b.p.errorf(f.Tag.Pos(), "empty jsonschema tag on field %s", id.Name)
				}
				if disallowedTag.MatchString(v) {
					return nil, b.p.errorf(f.Tag.Pos(), "jsonschema tag must not begin with 'WORD=': %q", v)
				}
				desc = v
			} else if f.Doc != nil {
				desc = strings.TrimSpace(f.Doc.Text())
			} else if f.Comment != nil {
				desc = strings.TrimSpace(f.Comment.Text())
			}
			if desc != "" {
				if fs.ContentEncoding == "base64" && !strings.Contains(desc, "base64") {
					desc += " (" + base64Note + ")"
				}
				fs.Description = desc
			}
			name := id.Name
			if jsonName != "" {
				name = jsonName
			}
			out = append(out, prop{
				name:     name,
				schema:   fs,
				required: !opts["omitempty"] && !opts["omitzero"],
				depth:    depth,
				tagged:   jsonName != "",
				pos:      id.Pos(),
			})
		}
	}
	return out, nil
}

func embeddedName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return embeddedName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return t.Sel.Name
	}
	return "?"
}

// embedsStruct reports whether e, an embedded field's type, resolves
// through declared names and aliases (type Alias = Base) to a struct
// declared in this file.
func (b *builder) embedsStruct(e ast.Expr) bool {
	_, _, ok := b.structDecl(e)
	return ok
}

// structDecl resolves e through pointers and declared names/aliases to a
// struct type, returning it and the declaring name.
func (b *builder) structDecl(e ast.Expr) (*ast.StructType, string, bool) {
	if star, ok := e.(*ast.StarExpr); ok {
		e = star.X
	}
	seen := map[string]bool{}
	for {
		id, ok := e.(*ast.Ident)
		if !ok || seen[id.Name] {
			return nil, "", false
		}
		seen[id.Name] = true
		d, ok := b.p.decls[id.Name]
		if !ok {
			return nil, "", false
		}
		if st, ok := d.spec.Type.(*ast.StructType); ok {
			return st, id.Name, true
		}
		e = d.spec.Type
		if star, ok := e.(*ast.StarExpr); ok {
			e = star.X
		}
	}
}

func (b *builder) embedded(e ast.Expr, depth int) ([]prop, error) {
	st, name, ok := b.structDecl(e)
	if !ok {
		return nil, b.p.errorf(e.Pos(), "unsupported embedded type %s: embed a struct declared in this file", gotypes.ExprString(e))
	}
	for _, n := range b.stack {
		if n == name {
			chain := append(append([]string(nil), b.stack...), name)
			return nil, b.p.errorf(e.Pos(), "recursive type %s (%s)", name, strings.Join(chain, " -> "))
		}
	}
	b.stack = append(b.stack, name)
	defer func() { b.stack = b.stack[:len(b.stack)-1] }()
	return b.fields(st, depth+1)
}

// jsonInfo reads a json struct tag the way encoding/json (and
// jsonschema.For) does.
func jsonInfo(tag reflect.StructTag) (name string, opts map[string]bool, omit bool) {
	v, ok := tag.Lookup("json")
	if !ok {
		return "", nil, false
	}
	name, rest, found := strings.Cut(v, ",")
	if name == "-" && !found {
		return "", nil, true
	}
	opts = map[string]bool{}
	if rest != "" {
		for _, o := range strings.Split(rest, ",") {
			opts[o] = true
		}
	}
	return name, opts, false
}
