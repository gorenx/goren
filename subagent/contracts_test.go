package subagent_test

import (
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	subagentplugin "github.com/gorenx/goren/subagent/plugin"
	"github.com/gorenx/goren/tools"
)

func TestStartCommandsKeepImplementationInputsSeparate(t *testing.T) {
	t.Parallel()
	label := "terminal"
	oneShot, err := subagent.NewOneShotStart(
		subagent.ChildRequest{},
		subagent.OneShotOptions{
			SeedBuilder: "spawn",
			Label:       &label,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	childID := session.SessionID("durable-child")
	continuable, err := subagent.NewContinuableStart(
		subagent.ChildRequest{},
		subagent.ContinuableOptions{
			SeedBuilder: "fork",
			Label:       "background",
			ChildID:     &childID,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if oneShot.Mode() != subagent.ModeOneShot {
		t.Fatalf("OneShot command = %#v", oneShot)
	}
	if continuable.Mode() != subagent.ModeContinuable ||
		continuable.RequestedChildID() == nil ||
		*continuable.RequestedChildID() != childID {
		t.Fatalf("Continuable command = %#v", continuable)
	}
	label = "changed"
	childID = "changed"
	if oneShot.Label() == nil || *oneShot.Label() != "terminal" ||
		*continuable.RequestedChildID() != "durable-child" {
		t.Fatal("StartCommand retained caller-owned pointers")
	}
}

func TestStartCommandOwnsItsChildRequest(t *testing.T) {
	t.Parallel()
	maxTokens := 128
	maxDepth := int64(3)
	persona := "reviewer"
	input := subagent.ChildRequest{
		Prompt: []agentmessage.ContentBlock{
			agentmessage.NewTextBlock("inspect"),
		},
		AgentOptions: &agent.Options{
			Provider:  "provider",
			Model:     "model",
			MaxTokens: &maxTokens,
		},
		MaxDepth: &maxDepth,
		ToolFilter: &tools.ToolRestriction{
			Allow: []string{"read"},
			Deny:  []string{"write"},
		},
		Persona: &persona,
	}
	command, commandErr := subagent.NewOneShotStart(
		input,
		subagent.OneShotOptions{
			SeedBuilder: "spawn",
		},
	)
	if commandErr != nil {
		t.Fatal(commandErr)
	}
	maxTokens = 1
	maxDepth = 1
	persona = "changed"
	input.ToolFilter.Allow[0] = "changed"
	input.ToolFilter.Deny[0] = "changed"
	firstRead, requestErr := command.Request()
	if requestErr != nil {
		t.Fatal(requestErr)
	}
	if *firstRead.AgentOptions.MaxTokens != 128 || *firstRead.MaxDepth != 3 ||
		*firstRead.Persona != "reviewer" || firstRead.ToolFilter.Allow[0] != "read" ||
		firstRead.ToolFilter.Deny[0] != "write" {
		t.Fatalf("command changed with caller-owned input: %#v", firstRead)
	}
	firstRead.ToolFilter.Allow[0] = "mutated"
	secondRead, requestErr := command.Request()
	if requestErr != nil {
		t.Fatal(requestErr)
	}
	if secondRead.ToolFilter.Allow[0] != "read" {
		t.Fatalf("Request exposed command state: %#v", secondRead)
	}
}

func TestStartCommandRejectsInvalidChildRequest(t *testing.T) {
	t.Parallel()
	negativeDepth := int64(-1)
	testCases := []struct {
		name  string
		input subagent.ChildRequest
	}{
		{
			name: "negative max depth",
			input: subagent.ChildRequest{
				MaxDepth: &negativeDepth,
			},
		},
		{
			name: "empty tool restriction",
			input: subagent.ChildRequest{
				ToolFilter: &tools.ToolRestriction{},
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			_, commandErr := subagent.NewOneShotStart(
				testCase.input,
				subagent.OneShotOptions{
					SeedBuilder: "spawn",
				},
			)
			if commandErr == nil {
				t.Fatal("invalid child request was accepted")
			}
		})
	}
}

func TestSessionSeedOwnsItsEventSnapshot(t *testing.T) {
	t.Parallel()
	sourceSequences := []int64{3}
	surfaceOperation := session.SurfaceReplace(1, 2)
	sourceEvents := []session.Event{
		{
			Type:            session.TurnEndEventName,
			Data:            []byte(`{"value":"original"}`),
			SourceEventSeqs: &sourceSequences,
			SurfaceOp:       &surfaceOperation,
		},
	}
	seed := subagent.NewSessionSeed(sourceEvents)
	sourceEvents[0].Data[0] = 'x'
	(*sourceEvents[0].SourceEventSeqs)[0] = 9
	sourceEvents[0].SurfaceOp.Start = 9

	firstRead := seed.EventPrefix()
	if string(firstRead[0].Data) != `{"value":"original"}` ||
		(*firstRead[0].SourceEventSeqs)[0] != 3 ||
		firstRead[0].SurfaceOp.Start != 1 {
		t.Fatalf("seed retained caller-owned event state: %#v", firstRead)
	}
	firstRead[0].Data[0] = 'y'
	(*firstRead[0].SourceEventSeqs)[0] = 7
	firstRead[0].SurfaceOp.Start = 7
	secondRead := seed.EventPrefix()
	if string(secondRead[0].Data) != `{"value":"original"}` ||
		(*secondRead[0].SourceEventSeqs)[0] != 3 ||
		secondRead[0].SurfaceOp.Start != 1 {
		t.Fatalf("seed exposed mutable event state: %#v", secondRead)
	}
}

func TestDescriptorAndLifecycleVocabulary(t *testing.T) {
	t.Parallel()
	if !session.IsKnownEventType(subagent.DescriptorEventName) {
		t.Fatal("descriptor event type is not registered")
	}
	if (subagent.SeedBuilderAdded{}).EventDelivery() != plugin.DeliveryOrdered {
		t.Fatal("SeedBuilderAdded must remain vetoable")
	}
	if (subagent.SeedBuilderRemoved{}).EventDelivery() != plugin.DeliveryBestEffort {
		t.Fatal("SeedBuilderRemoved must contain observer failures")
	}
	if (subagent.Started{}).EventName() != "subagent/start" ||
		(subagent.Ended{}).EventName() != "subagent/end" {
		t.Fatal("execution lifecycle event names drifted")
	}
}

func TestRuntimeProvidesOnlyPublicBusinessCapabilities(t *testing.T) {
	t.Parallel()
	providedNames := map[string]bool{}
	for _, capabilityType := range subagentplugin.New(
		subagentplugin.Diagnostics{},
	).Manifest().Provides {
		providedNames[capabilityType.Name()] = true
	}
	wantedTypes := []plugin.ServiceType{
		plugin.ServiceOf[subagent.SeedBuilderRegistry](),
		plugin.ServiceOf[subagent.Starter](),
		plugin.ServiceOf[subagent.ChildControl](),
		plugin.ServiceOf[subagent.ExtensionRegistry](),
		plugin.ServiceOf[subagent.ChildDirectory](),
	}
	for _, wantedType := range wantedTypes {
		if !providedNames[wantedType.Name()] {
			t.Fatalf("Runtime does not provide %q", wantedType.Name())
		}
	}
}
