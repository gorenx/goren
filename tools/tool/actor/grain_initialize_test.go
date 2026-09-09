package toolactor_test

import (
	"context"
	"testing"

	"github.com/gorenx/goren/toolaskuser"
	"github.com/gorenx/goren/tools"
	catalogactor "github.com/gorenx/goren/tools/registry/actor"
	toolactor "github.com/gorenx/goren/tools/tool/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

func TestInitializationRetryConfirmsBindingWithoutReplacingImplementation(t *testing.T) {
	system, directory, target := startTool(t)
	input := toolactor.Initialize{
		CreationID: "creation",
		AgentID:    "conversation",
		Name:       toolaskuser.Name,
		Authority:  toolAuthority(t, system),
	}
	if reply := ask[toolactor.Completion](t, system, target, input); reply.Err != nil {
		t.Fatalf("same binding retry rejected: %v", reply.Err)
	}
	source := &provider{}
	provisionTool(t, system, directory, target, source)
	if reply := ask[toolactor.Completion](t, system, target, input); reply.Err != nil {
		t.Fatal(reply.Err)
	}
	retained := invocation(currentIdentity(t, system, target))
	committed := ask[toolactor.Completion](t, system, target, toolactor.Commit{
		CreationID: "creation",
		Revision:   "initial",
	})
	if committed.Err != nil || currentIdentity(t, system, target) != retained.Implementation {
		t.Fatal("commit retry replaced the implementation identity", committed.Err)
	}

	if reply := ask[tools.BodyResult](t, system, target, retained); reply.Err != nil || source.opens.Load() != 1 {
		t.Fatalf("binding retry replaced implementation: %v, opens = %d", reply.Err, source.opens.Load())
	}
	other, err := goakt.GrainOf[*catalogactor.RegistryGrain](context.Background(), system, "other-catalog", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"creation", "agent", "name", "authority"} {
		changed := input
		switch field {
		case "creation":
			changed.CreationID = "other"
		case "agent":
			changed.AgentID = "other"
		case "name":
			changed.Name = "other"
		case "authority":
			changed.Authority = other
		}
		if reply := ask[toolactor.Completion](t, system, target, changed); reply.Err == nil {
			t.Fatalf("conflicting %s binding accepted", field)
		}
	}
	if reply := ask[toolactor.Completion](t, system, target, toolactor.Close{
		CreationID: "creation",
	}); reply.Err != nil {
		t.Fatal(reply.Err)
	}
	if reply := ask[toolactor.Completion](t, system, target, input); reply.Err != nil {
		t.Fatal(reply.Err)
	}
	if reply := ask[tools.BodyResult](t, system, target, retained); reply.Err == nil || source.closes.Load() != 1 {
		t.Fatal("binding acknowledgement revived closed resources")
	}
}
