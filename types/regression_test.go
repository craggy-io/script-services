package types_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/craggy-io/script-services/types"
)

func TestEnumRangeAndFiniteChecks(t *testing.T) {
	cases := []struct{ src, name, want string }{
		{"package a\n\n// enum: NaN\ntype F float64\n", "F", "not a finite number"},
		{"package a\n\n// enum: +Inf\ntype F float64\n", "F", "not a finite number"},
		{"package a\n\n// enum: 999\ntype N int8\n", "N", "out of range"},
		{"package a\n\n// enum: -1\ntype N uint\n", "N", "out of range"},
		{"package a\n\n// enum: 256\ntype N uint8\n", "N", "out of range"},
	}
	for _, c := range cases {
		pkg := parse(t, c.src)
		_, err := pkg.Schema(c.name)
		var te *types.Error
		if err == nil || !strings.Contains(err.Error(), c.want) || !errors.As(err, &te) {
			t.Errorf("%s: err = %v", c.name, err)
		}
	}
	pkg := parse(t, "package a\n\n// enum: 18446744073709551615\ntype N uint64\n")
	if got := schemaJSON(t, pkg, "N"); !strings.Contains(got, `"enum":[18446744073709551615]`) {
		t.Fatalf("uint64 enum = %s", got)
	}
}

func TestEnumQuotedCommas(t *testing.T) {
	pkg := parse(t, "package a\n\n// enum: \"a,b\", c\ntype S string\n")
	if got := schemaJSON(t, pkg, "S"); !strings.Contains(got, `"enum":["a,b","c"]`) {
		t.Fatalf("quoted comma enum = %s", got)
	}
}

func TestDuplicateTypeDeclaration(t *testing.T) {
	if _, err := types.Parse("types.go", []byte("package a\n\ntype T string\ntype T int\n")); err == nil || !strings.Contains(err.Error(), "declared twice") {
		t.Fatalf("duplicate decl err = %v", err)
	}
}

func TestTaggedUnexportedAndNonStructEmbeds(t *testing.T) {
	pkg := parse(t, `package a

type base struct {
	ID string `+"`json:\"id\"`"+`
}

type Count int

type Base struct {
	Name string `+"`json:\"name\"`"+`
}

type Alias = Base

type S struct {
	base  `+"`json:\"base\"`"+`
	Count
	Alias
}
`)
	got := schemaJSON(t, pkg, "S")
	for _, want := range []string{`"base":`, `"id":`, `"Count":`, `"name":`} {
		if !strings.Contains(got, want) {
			t.Fatalf("schema %s missing %s", got, want)
		}
	}
}
