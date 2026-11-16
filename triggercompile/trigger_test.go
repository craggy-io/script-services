package triggercompile_test

import (
	"context"
	"testing"

	"github.com/craggy-io/script-services/script"
	"github.com/craggy-io/script-services/triggercompile"
)

func TestAuthoring(t *testing.T) {
	for _, source := range []string{
		`T = on_event("files.created@1", where=all_of(eq("data.form", "blob"), ends_with("data.name", ".so")), input={"subject":event_field("data.id")})`,
		`T = on_event("files.created@1", where=not_(exists("data.deleted")), manual=True, deduplicate=["subject"])`,
	} {
		_, err := script.Exec(context.Background(), script.Program{Filename: "trigger.star", Source: []byte(source), Predeclared: triggercompile.Builtins()}, script.Options{Limits: script.DefaultLimits})
		if err != nil {
			t.Fatal(err)
		}
	}
}
