// Package triggercompile implements the shared, side-effect-free Starlark
// trigger vocabulary. All hosts receive foundation-kit trigger specifications.
package triggercompile

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/craggy-io/foundation-kit/trigger"
	"github.com/craggy-io/script-services/values"
	"go.starlark.net/starlark"
)

// Value is an immutable authored trigger.
type Value struct{ Spec trigger.Spec }

func (v *Value) String() string        { return "on_event(" + v.Spec.Event + ")" }
func (v *Value) Type() string          { return "trigger" }
func (v *Value) Freeze()               {}
func (v *Value) Truth() starlark.Bool  { return true }
func (v *Value) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable: trigger") }

type expression struct {
	predicate *trigger.Predicate
	mapping   *trigger.Mapping
}

func (v *expression) String() string        { return "trigger expression" }
func (v *expression) Type() string          { return "trigger_expression" }
func (v *expression) Freeze()               {}
func (v *expression) Truth() starlark.Bool  { return true }
func (v *expression) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable: trigger expression") }

// Builtins returns fresh declarations; no host state is captured.
func Builtins() starlark.StringDict {
	out := starlark.StringDict{"on_event": starlark.NewBuiltin("on_event", onEvent), "event_field": starlark.NewBuiltin("event_field", field)}
	for _, op := range []string{"eq", "ends_with", "exists", "all_of", "any_of", "not_"} {
		out[op] = starlark.NewBuiltin(op, predicate)
	}
	for name, value := range Sources() {
		out[name] = value
	}
	return out
}

// Names is the corresponding static checker vocabulary.
func Names() []string {
	var out []string
	for n := range Builtins() {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// List validates a list or tuple of authored triggers and detaches its values.
func List(v starlark.Value) ([]trigger.Spec, error) {
	if v == nil || v == starlark.None {
		return nil, nil
	}
	items, ok := v.(starlark.Iterable)
	if !ok {
		return nil, fmt.Errorf("triggers must be a sequence")
	}
	iter := items.Iterate()
	defer iter.Done()
	var item starlark.Value
	var out []trigger.Spec
	for iter.Next(&item) {
		t, ok := item.(*Value)
		if !ok {
			source, yes := item.(*SourceValue)
			if !yes || (source.Source.Kind != "every" && source.Source.Kind != "cron") {
				return nil, fmt.Errorf("workflow triggers must be on_event, every or cron declarations")
			}
			t = &Value{Spec: trigger.Spec{Event: "clock.tick@1", Clock: &source.Source}}
		}
		if err := t.Spec.Validate(); err != nil {
			return nil, err
		}
		b, _ := json.Marshal(t.Spec)
		var spec trigger.Spec
		_ = json.Unmarshal(b, &spec)
		out = append(out, spec)
	}
	return out, nil
}

func field(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var path string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "path", &path); err != nil {
		return nil, err
	}
	return &expression{mapping: &trigger.Mapping{Path: path}}, nil
}

func predicate(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	p := trigger.Predicate{Op: b.Name()}
	switch b.Name() {
	case "all_of", "any_of", "not_":
		if len(kwargs) > 0 {
			return nil, fmt.Errorf("%s takes positional predicates", b.Name())
		}
		p.Op = map[string]string{"all_of": "all", "any_of": "any", "not_": "not"}[b.Name()]
		for _, arg := range args {
			ex, ok := arg.(*expression)
			if !ok || ex.predicate == nil {
				return nil, fmt.Errorf("%s expects predicates", b.Name())
			}
			p.Args = append(p.Args, *ex.predicate)
		}
	default:
		var value starlark.Value
		if p.Op == "exists" {
			if err := starlark.UnpackArgs(b.Name(), args, kwargs, "path", &p.Path); err != nil {
				return nil, err
			}
		} else {
			if err := starlark.UnpackArgs(b.Name(), args, kwargs, "path", &p.Path, "value", &value); err != nil {
				return nil, err
			}
			raw, err := values.ToJSON(value)
			if err != nil {
				return nil, err
			}
			p.Value = raw
		}
	}
	return &expression{predicate: &p}, nil
}

func onEvent(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	s := trigger.Spec{}
	var where, dedup starlark.Value
	var input *starlark.Dict
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "event", &s.Event, "where?", &where, "input?", &input, "deduplicate?", &dedup, "manual?", &s.Manual, "replay?", &s.Replay); err != nil {
		return nil, err
	}
	if where != nil {
		p, ok := where.(*expression)
		if !ok || p.predicate == nil {
			return nil, fmt.Errorf("where expects a predicate")
		}
		s.Where = *p.predicate
	}
	if input != nil {
		s.Input = map[string]trigger.Mapping{}
		for _, pair := range input.Items() {
			name, ok := starlark.AsString(pair[0])
			if !ok {
				return nil, fmt.Errorf("input keys must be strings")
			}
			if ex, ok := pair[1].(*expression); ok && ex.mapping != nil {
				s.Input[name] = *ex.mapping
			} else {
				raw, err := values.ToJSON(pair[1])
				if err != nil {
					return nil, err
				}
				s.Input[name] = trigger.Mapping{Value: raw}
			}
		}
	}
	if dedup != nil {
		raw, err := values.ToJSON(dedup)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &s.Deduplicate); err != nil {
			return nil, fmt.Errorf("deduplicate expects field paths: %w", err)
		}
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &Value{Spec: s}, nil
}
