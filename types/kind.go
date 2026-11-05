package types

import (
	"context"
	"errors"
	"go/scanner"
	"go/token"
	"io/fs"
	"strings"

	"github.com/craggy-io/foundation-kit/bundle"
	"github.com/craggy-io/foundation-kit/kind"
)

// Declarations is the kind of a Go-syntax type declaration file in a bundle,
// craggy.types@1. Its value is the parsed Package, every one of whose types
// derived a schema without error.
var Declarations = kind.New[*Package]("craggy.types", 1)

// CodeInvalid is the diagnostic for a declaration file that does not parse,
// or a type in it that has no schema.
const CodeInvalid bundle.Code = "types.invalid"

// Compiler returns the compiler for Declarations. A contribution's path must
// name a .go file.
func Compiler() bundle.AnyCompiler {
	return bundle.Compile(Declarations, compile)
}

func compile(_ context.Context, c bundle.Contribution) (bundle.Compiled[*Package], []bundle.Diagnostic) {
	diag := func(pos token.Position, msg string) bundle.Diagnostic {
		return bundle.Diagnostic{Code: CodeInvalid, Severity: bundle.SeverityError, Bundle: c.Bundle.ID,
			Contribution: c.Ref.ID, Path: c.Path, Line: pos.Line, Column: pos.Column, Message: msg}
	}
	if !strings.HasSuffix(c.Path, ".go") {
		return bundle.Compiled[*Package]{}, []bundle.Diagnostic{diag(token.Position{}, "type declarations "+c.Path+" must be a .go file")}
	}
	src, err := fs.ReadFile(c.Files, c.Path)
	if err != nil {
		return bundle.Compiled[*Package]{}, []bundle.Diagnostic{diag(token.Position{}, err.Error())}
	}
	pkg, err := Parse(c.Path, src)
	if err != nil {
		var list scanner.ErrorList
		var te *Error
		switch {
		case errors.As(err, &list):
			out := make([]bundle.Diagnostic, 0, len(list))
			for _, e := range list {
				out = append(out, diag(e.Pos, e.Msg))
			}
			return bundle.Compiled[*Package]{}, out
		case errors.As(err, &te):
			return bundle.Compiled[*Package]{}, []bundle.Diagnostic{diag(te.Pos, te.Msg)}
		}
		return bundle.Compiled[*Package]{}, []bundle.Diagnostic{diag(token.Position{}, err.Error())}
	}
	var diags []bundle.Diagnostic
	for _, name := range pkg.Names() {
		if _, err := pkg.Schema(name); err != nil {
			var te *Error
			if errors.As(err, &te) {
				diags = append(diags, diag(te.Pos, te.Msg))
			} else {
				diags = append(diags, diag(token.Position{}, err.Error()))
			}
		}
	}
	if len(diags) > 0 {
		return bundle.Compiled[*Package]{}, diags
	}
	return bundle.Compiled[*Package]{Value: pkg}, nil
}
