package modules

import (
	"context"
	"fmt"
	"io/fs"
	"strings"

	"github.com/craggy-io/foundation-kit/bundle"
)

// FromContribution is the Source for a script contribution of a bundle (see
// package bundle in foundation-kit). It serves:
//
//   - canonical paths in the main tree ("lib/x.star", from "./", "../", "//"
//     and bare specs) from the contribution's own bundle, c.Files;
//   - "@alias/x.star" from the bundle c.Dependency(aliases[alias]) returns,
//     or c.Dependency(alias) when aliases has no entry for alias, so a
//     dependency's bundle ID works as its alias.
//
// The dependency must be declared under dependencies in the contribution's
// bundle.yaml. An alias that names no declared dependency fails to read with
// an error naming the alias; it does not wrap fs.ErrNotExist. Inside a
// dependency, "@alias" specs use the same aliases, and therefore the
// dependencies the contribution's bundle declares: a dependency's own
// dependencies are not visible. Nothing is looked up across several file
// systems in order.
//
// Bundle snapshots are immutable, so the Source is safe for concurrent use
// and stays valid for as long as c is retained, including after Load
// returns.
func FromContribution(c bundle.Contribution, aliases map[string]string) Source {
	copied := make(map[string]string, len(aliases))
	for k, v := range aliases {
		copied[k] = v
	}
	return contributionSource{c: c, aliases: copied}
}

type contributionSource struct {
	c       bundle.Contribution
	aliases map[string]string
}

func (s contributionSource) Read(ctx context.Context, p string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fsys, rel := s.c.Files, p
	if strings.HasPrefix(p, "@") {
		tree, rest, _ := strings.Cut(p, "/")
		alias := tree[1:]
		id, ok := s.aliases[alias]
		if !ok {
			id = alias
		}
		files, _, ok := s.c.Dependency(id)
		if !ok {
			if id != alias {
				return nil, fmt.Errorf("load alias @%s names dependency %s, which bundle %s does not declare or which is not loaded", alias, id, s.c.Bundle.ID)
			}
			return nil, fmt.Errorf("load alias @%s: no such alias, and no dependency %s that bundle %s declares and that is loaded", alias, alias, s.c.Bundle.ID)
		}
		fsys, rel = files, rest
	}
	if !fs.ValidPath(rel) {
		return nil, &fs.PathError{Op: "read", Path: p, Err: fs.ErrInvalid}
	}
	return fs.ReadFile(fsys, rel)
}
