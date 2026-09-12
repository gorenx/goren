package tools_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/tools"
)

func TestAgentCreationContextPreservesScopeAndCancellation(t *testing.T) {
	type traceKey struct{}
	deadline := time.Now().Add(time.Minute)
	parent, cancel := context.WithDeadline(context.WithValue(t.Context(), traceKey{}, "trace"), deadline)
	defer cancel()
	previous := tools.WithAgentCreationID(parent, "previous")
	current := tools.WithAgentCreationID(previous, "current")
	for _, expected := range []struct {
		ctx        context.Context
		identifier string
	}{
		{
			ctx:        previous,
			identifier: "previous",
		},
		{
			ctx:        current,
			identifier: "current",
		},
	} {
		identifier, found := tools.AgentCreationIDFromContext(expected.ctx)
		if !found || identifier != expected.identifier {
			t.Fatal("derived identity mutated another scope")
		}
		limit, exists := expected.ctx.Deadline()
		if !exists || !limit.Equal(deadline) || expected.ctx.Value(traceKey{}) != "trace" {
			t.Fatal("identity propagation lost request metadata")
		}
	}
	for _, absent := range []context.Context{nil, parent, tools.WithAgentCreationID(current, "")} {
		if identifier, found := tools.AgentCreationIDFromContext(absent); found || identifier != "" {
			t.Fatal("missing identity must not inherit or fabricate a lifetime")
		}
	}
	cancel()
	if !errors.Is(context.Cause(current), context.Canceled) {
		t.Fatal("identity propagation lost cancellation")
	}
}
