package companionactor

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/gorenx/goren/agent"
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
	stewardProvider, first, firstInput := installParent(t, fixture, fixture.authority)
	awaitInstallation(t, fixture, stewardProvider, firstInput.Creation, Ready)
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
	awaitInstallation(t, fixture, stewardProvider, secondInput.Creation, Ready)
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
	awaitInstallation(t, fixture, stewardProvider, thirdInput.Creation, Ready)
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
		awaitInstallation(t, fixture, stewardProvider, input.Creation, Ready)
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
	stewardProvider, sender, input := installParent(t, fixture, fixture.authority)
	awaitInstallation(t, fixture, stewardProvider, input.Creation, Ready)
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
	current, err := fixture.creator.settings.Describe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.creator.settings.UpdateCompanion(t.Context(), "reviewer", current.State.Version(settings.Group{
		Section: settings.CompanionSection,
		Name:    "reviewer",
	}), companions.Config{
		Enabled:       true,
		Configuration: "review-template",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := sender.Restart(t.Context(), Restart{
		Name:               "reviewer",
		ExpectedRevision:   failed.Companion.Revision,
		ExpectedCreationID: failed.Creation.CreationID,
	}); err != nil {
		t.Fatal(err)
	}
	recovered := awaitPhase(t, sender, "reviewer", Ready)
	if !recovered.Persisted || recovered.Companion.ChildSessionID == "" {
		t.Fatalf("retry did not persist and start the prepared Companion: %+v", recovered)
	}
}

// retryCreator returns one preparation error, then exposes cancellation
// while deliberately retaining its result until the test releases it.
type retryCreator struct {
	ChildCreator
	attempts  atomic.Int32
	starts    atomic.Int32
	entered   chan struct{}
	cancelled chan struct{}
	release   chan struct{}
}

func (barrier *retryCreator) Resolve(ctx context.Context, _ agentregistry.Creation, _ companions.Config, values settings.Values) (settings.Values, error) {
	if barrier.attempts.Add(1) == 1 {
		return settings.Values{}, errors.New("test: initial preparation failed")
	}
	close(barrier.entered)
	<-ctx.Done()
	close(barrier.cancelled)
	<-barrier.release
	return values.Clone(), nil
}

func (barrier *retryCreator) Start(ctx context.Context, parent, child agentregistry.Creation, name string, cwd *string) (agentregistry.Ready, error) {
	barrier.starts.Add(1)
	return barrier.ChildCreator.Start(ctx, parent, child, name, cwd)
}

func TestDefaultPreparationRetrySettlesBeforeParentClose(t *testing.T) {
	fixture := newStewardship(t)
	if _, err := fixture.creator.settings.UpdateAgent(t.Context(), "retry-template", 0, settings.Values{}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.creator.settings.UpdateCompanion(t.Context(), "reviewer", 0, companions.Config{
		Enabled:       true,
		Configuration: "retry-template",
	}); err != nil {
		t.Fatal(err)
	}
	barrier := &retryCreator{
		ChildCreator: fixture.creator,
		entered:      make(chan struct{}),
		cancelled:    make(chan struct{}),
		release:      make(chan struct{}),
	}
	defer func() {
		select {
		case <-barrier.release:
		default:
			close(barrier.release)
		}
	}()
	stewardProvider, sender, input := installParent(t, fixture, fixture.authority, barrier)
	awaitInstallation(t, fixture, stewardProvider, input.Creation, Ready)
	awaitPhase(t, sender, "reviewer", StartFailed)
	if _, err := sender.Restart(t.Context(), Restart{
		Name: "reviewer",
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-barrier.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("retry did not start preparation")
	}
	if _, err := sender.Restart(t.Context(), Restart{
		Name: "reviewer",
	}); !errors.Is(err, agent.ErrBusy) {
		t.Fatalf("duplicate restart = %v", err)
	}
	if _, err := sender.Create(t.Context(), Create{
		Name:    "reviewer",
		Enabled: true,
	}); !errors.Is(err, companions.ErrConflict) {
		t.Fatalf("creation overlapped preparation: %v", err)
	}
	closed := make(chan error, 1)
	go func() {
		result, failure := ask[agentactor.Closed](t.Context(), fixture.system, fixture.authority, agentregistry.CloseAgent{
			Creation: input.Creation,
		})
		closed <- errors.Join(failure, result.Err)
	}()
	select {
	case <-barrier.cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("parent close did not cancel preparation")
	}
	select {
	case failure := <-closed:
		t.Fatalf("parent closed before preparation settled: %v", failure)
	default:
	}
	close(barrier.release)
	select {
	case failure := <-closed:
		if failure != nil {
			t.Fatal(failure)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("parent close did not settle")
	}
	if barrier.starts.Load() != 0 {
		t.Fatal("late preparation started a child after parent close")
	}
}

func TestDefaultCompanionWithoutNamedValuesUsesCapabilityDefaults(t *testing.T) {
	fixture := newStewardship(t)
	if _, err := fixture.creator.settings.UpdateCompanion(t.Context(), "minimal", 0, companions.Config{
		Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	stewardProvider, sender, input := installParent(t, fixture, fixture.authority)
	awaitInstallation(t, fixture, stewardProvider, input.Creation, Ready)
	current := awaitPhase(t, sender, "minimal", Ready)
	if !current.Persisted || current.Companion.Configuration == "" {
		t.Fatalf("default configuration was not saved for the child: %+v", current)
	}
}
