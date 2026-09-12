package companionactor

import (
	"context"
	"testing"
	"time"

	agentactor "github.com/gorenx/goren/agent/actor"
	agentregistry "github.com/gorenx/goren/agent/registry"
	"github.com/gorenx/goren/settings"
	companions "github.com/gorenx/goren/subagent/companion"
)

func TestDefaultCompanionsStayIsolatedAndRespectDurableTombstones(t *testing.T) {
	fixture := newStewardship(t)
	if _, err := fixture.creator.settings.UpdateAgent(t.Context(), "review-template", 0, settings.Values{}); err != nil {
		t.Fatal(err)
	}
	view, err := fixture.creator.settings.UpdateCompanion(t.Context(), "reviewer", 0, companions.Config{
		Enabled:       true,
		Configuration: "review-template",
	})
	if err != nil {
		t.Fatal(err)
	}
	version := view.State.Version(settings.Group{
		Section: settings.CompanionSection,
		Name:    "reviewer",
	})
	installer, first, firstInput := installParent(t, fixture, fixture.authority)
	awaitInstallation(t, fixture, installer, firstInput.Creation, Ready)
	firstChild := awaitPhase(t, first, "reviewer", Ready)
	secondInput := firstInput
	secondInput.Creation = agentregistry.Creation{
		AgentID:    "another-parent",
		CreationID: "another-first",
	}
	secondReady := request[agentregistry.Ready](t, fixture.system, fixture.authority, secondInput)
	if secondReady.Err != nil {
		t.Fatal(secondReady.Err)
	}
	second := &Client{
		system: fixture.system,
		target: secondReady.Components[0].Runtime,
		parent: secondInput.Creation,
	}
	awaitInstallation(t, fixture, installer, secondInput.Creation, Ready)
	secondChild := awaitPhase(t, second, "reviewer", Ready)
	if firstChild.Companion.ChildSessionID == secondChild.Companion.ChildSessionID ||
		firstChild.Companion.Configuration == secondChild.Companion.Configuration ||
		firstChild.Companion.SourceRevision != version || secondChild.Companion.SourceRevision != version {
		t.Fatalf("default policy did not fix independent instances: %+v %+v", firstChild, secondChild)
	}
	if _, err := fixture.creator.settings.UpdateAgent(t.Context(), "new-template", 0, settings.Values{}); err != nil {
		t.Fatal(err)
	}
	updated, err := fixture.creator.settings.UpdateCompanion(t.Context(), "reviewer", version, companions.Config{
		Enabled:       true,
		Configuration: "new-template",
	})
	if err != nil {
		t.Fatal(err)
	}
	currentVersion := updated.State.Version(settings.Group{
		Section: settings.CompanionSection,
		Name:    "reviewer",
	})
	thirdInput := firstInput
	thirdInput.Creation = agentregistry.Creation{
		AgentID:    "later-parent",
		CreationID: "later-first",
	}
	thirdReady := request[agentregistry.Ready](t, fixture.system, fixture.authority, thirdInput)
	if thirdReady.Err != nil {
		t.Fatal(thirdReady.Err)
	}
	third := &Client{
		system: fixture.system,
		target: thirdReady.Components[0].Runtime,
		parent: thirdInput.Creation,
	}
	awaitInstallation(t, fixture, installer, thirdInput.Creation, Ready)
	thirdChild := awaitPhase(t, third, "reviewer", Ready)
	if thirdChild.Companion.SourceRevision != currentVersion ||
		thirdChild.Companion.ChildSessionID == firstChild.Companion.ChildSessionID ||
		thirdChild.Companion.Configuration == firstChild.Companion.Configuration {
		t.Fatal("new parent did not select the new default version")
	}
	firstAgain := awaitPhase(t, first, "reviewer", Ready)
	if firstAgain.Companion.SourceRevision != version || firstAgain.Companion.ChildSessionID != firstChild.Companion.ChildSessionID {
		t.Fatal("global policy update changed an existing Companion")
	}
	if _, err := first.Configure(t.Context(), Configure{
		Name:             "reviewer",
		ExpectedRevision: firstChild.Companion.Revision,
		Enabled:          false,
	}); err != nil {
		t.Fatal(err)
	}
	awaitPhase(t, first, "reviewer", Stopped)
	if _, err := second.Remove(t.Context(), Remove{
		Name:             "reviewer",
		ExpectedRevision: secondChild.Companion.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	awaitPhase(t, second, "reviewer", Stopped)
	for _, observed := range []struct {
		client   *Client
		input    agentregistry.CreateAgent
		removed  bool
		previous View
	}{
		{
			client:   first,
			input:    firstInput,
			previous: firstChild,
		},
		{
			client:   second,
			input:    secondInput,
			removed:  true,
			previous: secondChild,
		},
	} {
		closed := request[agentactor.Closed](t, fixture.system, fixture.authority, agentregistry.CloseAgent{
			Creation: observed.input.Creation,
		})
		if closed.Err != nil {
			t.Fatal(closed.Err)
		}
		input := observed.input
		input.Creation.CreationID += "-restored"
		input.Restore = true
		ready := request[agentregistry.Ready](t, fixture.system, fixture.authority, input)
		if ready.Err != nil {
			t.Fatal(ready.Err)
		}
		active := &Client{
			system: fixture.system,
			target: ready.Components[0].Runtime,
			parent: input.Creation,
		}
		awaitInstallation(t, fixture, installer, input.Creation, Ready)
		retained := awaitPhase(t, active, "reviewer", Stopped)
		if retained.Companion.ChildSessionID != observed.previous.Companion.ChildSessionID ||
			retained.Companion.Removed != observed.removed ||
			retained.Companion.Enabled || retained.Creation.CreationID != "" {
			t.Fatalf("recovery recreated a disabled or removed Companion: %+v", retained)
		}
		if observed.removed {
			secondInput = input
		} else {
			firstInput = input
		}
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, current := range []agentregistry.Creation{firstInput.Creation, secondInput.Creation, thirdInput.Creation} {
			closed, err := ask[agentactor.Closed](ctx, fixture.system, fixture.authority, agentregistry.CloseAgent{
				Creation: current,
			})
			if err != nil || closed.Err != nil {
				t.Errorf("restored parent cleanup: %v %v", err, closed.Err)
			}
		}
	})
}

func TestDefaultPreparationFailureIsVisibleWithoutCreatingChild(t *testing.T) {
	fixture := newStewardship(t)
	if _, err := fixture.creator.settings.UpdateAgent(t.Context(), "review-template", 0, settings.Values{}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.creator.settings.UpdateCompanion(t.Context(), "reviewer", 0, companions.Config{
		Enabled:       true,
		Configuration: "review-template",
		Inherit: companions.Inheritance{
			Tools: companions.InheritAllTools,
		},
	}); err != nil {
		t.Fatal(err)
	}
	installer, sender, input := installParent(t, fixture, fixture.authority)
	awaitInstallation(t, fixture, installer, input.Creation, Ready)
	failed := awaitPhase(t, sender, "reviewer", StartFailed)
	if failed.Err == nil || failed.Persisted || failed.Creation.CreationID != "" {
		t.Fatalf("unresolved policy started an Agent or hid failure: %+v", failed)
	}
	missing := request[agentregistry.Ready](t, fixture.system, fixture.authority, agentregistry.FindAgent{
		AgentID: failed.Companion.ChildSessionID,
	})
	if missing.Err == nil {
		t.Fatal("preparation failure admitted a child")
	}
}
