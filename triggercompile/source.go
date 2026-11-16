package triggercompile

import (
	"fmt"
	"github.com/craggy-io/foundation-kit/trigger"
	"go.starlark.net/starlark"
)

// SourceValue is an adapter declaration shared by scheduling hosts.
type SourceValue struct{ Source trigger.Source }

func (v *SourceValue) String() string        { return "trigger_source(" + v.Source.Kind + ")" }
func (v *SourceValue) Type() string          { return "trigger_source" }
func (v *SourceValue) Freeze()               {}
func (v *SourceValue) Truth() starlark.Bool  { return true }
func (v *SourceValue) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable: trigger source") }

// Sources returns timer and external-source declarations, without running them.
func Sources() starlark.StringDict {
	out := starlark.StringDict{}
	for _, name := range []string{"every", "cron", "watch", "on_push", "on_change"} {
		out[name] = starlark.NewBuiltin(name, source)
	}
	return out
}

func source(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	s := trigger.Source{Kind: b.Name()}
	var err error
	switch b.Name() {
	case "every":
		var interval string
		err = starlark.UnpackArgs(b.Name(), args, kwargs, "interval", &interval)
		if err == nil {
			s.Interval, err = trigger.ParseDuration(interval)
		}
	case "cron":
		err = starlark.UnpackArgs(b.Name(), args, kwargs, "expr", &s.Expr)
	case "watch":
		err = starlark.UnpackArgs(b.Name(), args, kwargs, "root", &s.Root, "glob?", &s.Glob)
	case "on_push":
		s.Kind = "push"
		err = starlark.UnpackArgs(b.Name(), args, kwargs)
	case "on_change":
		s.Kind = "change"
		err = starlark.UnpackArgs(b.Name(), args, kwargs, "upstream", &s.Upstream)
	}
	if err != nil {
		return nil, err
	}
	if err = s.Validate(); err != nil {
		return nil, err
	}
	return &SourceValue{Source: s}, nil
}

// SourceOf converts either an event declaration or an adapter declaration.
func SourceOf(v starlark.Value) (trigger.Source, error) {
	switch v := v.(type) {
	case *SourceValue:
		return v.Source, v.Source.Validate()
	case *Value:
		return trigger.Source{Kind: "event", Event: &v.Spec}, v.Spec.Validate()
	default:
		return trigger.Source{}, fmt.Errorf("expected a trigger declaration")
	}
}
