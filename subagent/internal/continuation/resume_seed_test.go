package continuation

import (
	"context"
	"testing"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/subagent"
)

func TestColdResumeReadsFirstDescriptorFromChildSuffix(t *testing.T) {
	t.Parallel()
	state := newManagerFixture(t)
	childID := session.SessionID("forked-continuable-child")
	ancestorLabel := "ancestor"
	ancestorData, snapshotErr := subagent.SnapshotDescriptor(
		subagent.OneShotDescriptor{
			Provider: "ancestor-spawn",
			Label:    &ancestorLabel,
		},
	)
	if snapshotErr != nil {
		t.Fatal(snapshotErr)
	}
	ancestorConversation, createErr := session.New(
		"ancestor-seed",
		session.CreateOptions{},
	)
	if createErr != nil {
		t.Fatal(createErr)
	}
	{
		var committedEvent session.Event
		var writeErr error
		draft, draftErr := session.NewEventDraft(subagent.DescriptorEvent,
			ancestorData)
		writeErr = draftErr
		if draftErr == nil {
			receipt, commitErr := ancestorConversation.Commit(context.Background(), session.Batch(draft))
			writeErr = commitErr
			if commitErr == nil {
				committedEvent = receipt.Events[0]
			}
		}
		if _, appendErr := committedEvent, writeErr; appendErr != nil {
			t.Fatal(appendErr)
		}
	}
	providerSeed := ancestorConversation.Events()
	childIdentity := subagent.ContinuableDescriptor{
		Provider:      "fork",
		Label:         "child",
		AgentProvider: stringPointer("deepseek"),
		AgentModel:    stringPointer("deepseek-chat"),
	}
	seed, seedErr := descriptorSeed(childID, providerSeed, childIdentity)
	if seedErr != nil {
		t.Fatal(seedErr)
	}
	lineageSeedLength := int64(len(providerSeed))
	depth := int64(1)
	storedSession, createErr := session.New(
		childID,
		session.CreateOptions{
			Seed: seed,
			Metadata: session.Metadata{
				ParentSession:   sessionIDReference(state.parent.ID()),
				SeedLength:      &lineageSeedLength,
				Origin:          session.OriginSubagent,
				DelegationDepth: &depth,
			},
		},
	)
	if createErr != nil {
		t.Fatal(createErr)
	}
	state.persistence.inspections[childID] = persistence.Inspection{
		Header: storedSession.Header(),
		Events: storedSession.Events(),
	}

	messageID, followErr := state.manager.Followup(
		context.Background(),
		state.parent,
		childID,
		[]llm.ContentBlock{
			llm.NewTextBlock("resume the child"),
		},
		subagent.FollowupOptions{
			Source: subagent.CoordinatorSource{
				SenderSessionID: state.parent.ID(),
			},
		},
	)
	if followErr != nil || messageID == "" {
		t.Fatalf("cold Followup = %q, %v", messageID, followErr)
	}
	resumed := state.agents.agents[childID]
	if resumed == nil {
		t.Fatal("cold resume did not publish the child")
	}
	if resumed.options.Provider != "deepseek" ||
		resumed.options.Model != "deepseek-chat" {
		t.Fatalf("resumed Agent options = %#v", resumed.options)
	}
	if len(state.lifecycle.started) != 1 ||
		state.lifecycle.started[0].Provider != "fork" {
		t.Fatalf("cold resume lifecycle = %#v", state.lifecycle.started)
	}
}
