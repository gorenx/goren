package bound

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
	sessionprojection "github.com/gorenx/goren/session/projection"
	"github.com/gorenx/goren/subagent"
	subagentprojection "github.com/gorenx/goren/subagent/internal/projection"
)

type boundConfigAgentRegistry struct {
	agent.Registry
	entries map[session.SessionID]agent.Agent
}

func (source *boundConfigAgentRegistry) Get(
	identifier session.SessionID,
) (agent.Agent, bool) {
	subject, found := source.entries[identifier]
	return subject, found
}

func (source *boundConfigAgentRegistry) Contains(subject agent.Agent) bool {
	if subject == nil {
		return false
	}
	current, found := source.entries[subject.ID()]
	return found && agent.Same(current, subject)
}

func (source *boundConfigAgentRegistry) List() []agent.Agent {
	values := make([]agent.Agent, 0, len(source.entries))
	for _, subject := range source.entries {
		values = append(values, subject)
	}
	return values
}

type boundConfigSessions struct {
	session.LiveStore
	mutex   sync.Mutex
	entries map[session.SessionID]session.Context
	flushes []session.SessionID
}

func (source *boundConfigSessions) Get(
	identifier session.SessionID,
) (session.Context, bool) {
	conversation, found := source.entries[identifier]
	return conversation, found
}

func (source *boundConfigSessions) Flush(
	_ context.Context,
	conversation session.Context,
) error {
	source.mutex.Lock()
	source.flushes = append(source.flushes, conversation.ID())
	source.mutex.Unlock()
	return nil
}

type boundConfigPersistence struct {
	persistence.Persistence
	inspections map[session.SessionID]persistence.Inspection
}

func (source *boundConfigPersistence) Inspect(
	_ context.Context,
	identifier session.SessionID,
) (persistence.Inspection, error) {
	inspection, found := source.inspections[identifier]
	if !found {
		return persistence.Inspection{}, &persistence.NotFoundError{
			ID: identifier,
		}
	}
	return inspection, nil
}

type boundConfigSeedRegistry struct {
	subagent.SeedBuilderRegistry
	builders map[string]subagent.SeedBuilder
}

func (source *boundConfigSeedRegistry) Find(
	builderName string,
) (subagent.SeedBuilder, bool) {
	builder, found := source.builders[builderName]
	return builder, found
}

type boundConfigSeedBuilder struct {
	name string
}

func (builder *boundConfigSeedBuilder) Name() string {
	return builder.name
}

func (*boundConfigSeedBuilder) ContextPolicy() subagent.ParentContextPolicy {
	return subagent.NoParentContext
}

func (*boundConfigSeedBuilder) BuildSeed(
	context.Context,
	[]session.Event,
) (subagent.SessionSeed, error) {
	return subagent.NewSessionSeed(nil), nil
}

type boundConfigFixture struct {
	owner       *Service
	parentAgent *boundAgent
	sessions    *boundConfigSessions
	projections *sessionprojection.DriveRegistry
}

func newBoundConfigFixture(t *testing.T) boundConfigFixture {
	t.Helper()
	parentAgent := newBoundAgent(t, "parent")
	projections := sessionprojection.NewDriveRegistry()
	for _, unit := range subagentprojection.Units() {
		if _, err := projections.Register(unit); err != nil {
			t.Fatal(err)
		}
	}
	sessions := &boundConfigSessions{
		entries: map[session.SessionID]session.Context{
			parentAgent.ID(): parentAgent.SessionValue(),
		},
	}
	owner, err := New(
		Dependencies{
			Agents: &boundConfigAgentRegistry{
				entries: map[session.SessionID]agent.Agent{
					parentAgent.ID(): parentAgent,
				},
			},
			Sessions:    sessions,
			Persistence: &boundConfigPersistence{},
			Projections: projections,
			SeedBuilders: &boundConfigSeedRegistry{
				builders: map[string]subagent.SeedBuilder{
					"spawn": &boundConfigSeedBuilder{
						name: "spawn",
					},
				},
			},
			Extensions: boundExtensionsRecord{
				validate: func(names []string) error {
					for _, extensionNameValue := range names {
						if extensionNameValue == "missing" {
							return &subagent.Error{
								Code:    subagent.ErrorUnknownExtension,
								Message: "missing Extension",
							}
						}
					}
					return nil
				},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return boundConfigFixture{
		owner:       owner,
		parentAgent: parentAgent,
		sessions:    sessions,
		projections: projections,
	}
}

func TestBindCommitsBindingAndInitialConfigBeforeChildCreation(t *testing.T) {
	t.Parallel()
	fixture := newBoundConfigFixture(t)
	childID := session.SessionID("bound-child")
	headerBefore := fixture.parentAgent.SessionValue().Header()
	result, err := fixture.owner.Bind(
		context.Background(),
		subagent.BindCommand{
			Parent:           fixture.parentAgent,
			RequestedChildID: &childID,
			SeedBuilder:      "spawn",
			Title:            "researcher",
			InitialPrompt: []agentmessage.ContentBlock{
				agentmessage.NewTextBlock("begin research"),
			},
			Config: subagent.BoundConfigInput{
				Enabled: true,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.ParentSessionID != fixture.parentAgent.ID() ||
		result.ChildSessionID != childID || result.ConfigRevision != 1 {
		t.Fatalf("Bind result = %#v", result)
	}
	events := fixture.parentAgent.SessionValue().Events()
	if len(events) != 2 ||
		events[0].Type != subagent.BoundBindingEventName ||
		events[1].Type != subagent.BoundConfigEventName {
		t.Fatalf("parent events = %#v", events)
	}
	view := boundView(t, fixture)
	binding, found := view.Binding(childID)
	if !found || binding.Creation.Title != "researcher" ||
		len(binding.Creation.InitialPrompt) != 1 {
		t.Fatalf("binding = %#v, found = %v", binding, found)
	}
	config, found := view.Config(childID)
	if !found || config.Revision != 1 || !config.Config.Enabled {
		t.Fatalf("config = %#v, found = %v", config, found)
	}
	if _, found = fixture.sessions.Get(childID); found {
		t.Fatal("Bind created a child Session")
	}
	if headerAfter := fixture.parentAgent.SessionValue().Header(); !reflect.DeepEqual(headerAfter, headerBefore) {
		t.Fatalf(
			"Bind changed parent Session header: before = %#v, after = %#v",
			headerBefore,
			headerAfter,
		)
	}
	fixture.sessions.mutex.Lock()
	flushes := append([]session.SessionID(nil), fixture.sessions.flushes...)
	fixture.sessions.mutex.Unlock()
	if !reflect.DeepEqual(flushes, []session.SessionID{"parent"}) {
		t.Fatalf("flushes = %v", flushes)
	}
}

func TestBindValidationCannotCommitPartialBinding(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name    string
		prompt  []agentmessage.ContentBlock
		extends []string
	}{
		{
			name: "missing initial prompt",
		},
		{
			name: "unknown Extension",
			prompt: []agentmessage.ContentBlock{
				agentmessage.NewTextBlock("start"),
			},
			extends: []string{"missing"},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			fixture := newBoundConfigFixture(t)
			childID := session.SessionID("bound-child")
			_, err := fixture.owner.Bind(
				context.Background(),
				subagent.BindCommand{
					Parent:           fixture.parentAgent,
					RequestedChildID: &childID,
					SeedBuilder:      "spawn",
					Title:            "researcher",
					InitialPrompt:    testCase.prompt,
					Config: subagent.BoundConfigInput{
						Enabled:    true,
						Extensions: testCase.extends,
					},
				},
			)
			if err == nil {
				t.Fatal("Bind accepted invalid input")
			}
			if events := fixture.parentAgent.SessionValue().Events(); len(events) != 0 {
				t.Fatalf("invalid Bind committed events = %#v", events)
			}
		})
	}
}

func TestUpdateConfigCommitsCompleteNextRevisionWithoutCreatingChild(
	t *testing.T,
) {
	t.Parallel()
	fixture := newBoundConfigFixture(t)
	childID := bindConfigChild(t, fixture, "researcher", "bound-child")
	persona := "careful reviewer"
	result, err := fixture.owner.UpdateConfig(
		context.Background(),
		subagent.UpdateBoundConfigCommand{
			Parent:           fixture.parentAgent,
			ChildSessionID:   childID,
			ExpectedRevision: 1,
			Config: subagent.BoundConfigInput{
				Enabled: true,
				Persona: &persona,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Revision != 2 {
		t.Fatalf("revision = %d", result.Revision)
	}
	config, found := boundView(t, fixture).Config(childID)
	if !found || config.Revision != 2 || config.Config.Persona == nil ||
		*config.Config.Persona != persona {
		t.Fatalf("config = %#v, found = %v", config, found)
	}
	if _, found = fixture.sessions.Get(childID); found {
		t.Fatal("UpdateConfig materialized a cold child")
	}
	before := len(fixture.parentAgent.SessionValue().Events())
	_, err = fixture.owner.UpdateConfig(
		context.Background(),
		subagent.UpdateBoundConfigCommand{
			Parent:           fixture.parentAgent,
			ChildSessionID:   childID,
			ExpectedRevision: 1,
			Config: subagent.BoundConfigInput{
				Enabled: true,
			},
		},
	)
	var typed *subagent.Error
	if !errors.As(err, &typed) || typed.Code != subagent.ErrorBoundConfigConflict {
		t.Fatalf("stale UpdateConfig error = %v", err)
	}
	if after := len(fixture.parentAgent.SessionValue().Events()); after != before {
		t.Fatalf("stale update event count = %d, want %d", after, before)
	}
}

func TestBindRejectsDuplicateParentTitle(t *testing.T) {
	t.Parallel()
	fixture := newBoundConfigFixture(t)
	bindConfigChild(t, fixture, "researcher", "bound-child-1")
	secondID := session.SessionID("bound-child-2")
	_, err := fixture.owner.Bind(
		context.Background(),
		subagent.BindCommand{
			Parent:           fixture.parentAgent,
			RequestedChildID: &secondID,
			SeedBuilder:      "spawn",
			Title:            "researcher",
			InitialPrompt: []agentmessage.ContentBlock{
				agentmessage.NewTextBlock("start"),
			},
			Config: subagent.BoundConfigInput{
				Enabled: true,
			},
		},
	)
	var typed *subagent.Error
	if !errors.As(err, &typed) ||
		typed.Code != subagent.ErrorDuplicateBoundBinding {
		t.Fatalf("duplicate title error = %v", err)
	}
}

func TestConcurrentBindSerializesTitleUniquenessAtSessionHead(t *testing.T) {
	t.Parallel()
	fixture := newBoundConfigFixture(t)
	const attempts = 16
	errorsByAttempt := make(chan error, attempts)
	var calls sync.WaitGroup
	for index := range attempts {
		calls.Add(1)
		go func() {
			defer calls.Done()
			childID := session.SessionID(fmt.Sprintf("bound-child-%d", index))
			_, err := fixture.owner.Bind(
				context.Background(),
				subagent.BindCommand{
					Parent:           fixture.parentAgent,
					RequestedChildID: &childID,
					SeedBuilder:      "spawn",
					Title:            "researcher",
					InitialPrompt: []agentmessage.ContentBlock{
						agentmessage.NewTextBlock("start"),
					},
					Config: subagent.BoundConfigInput{
						Enabled: true,
					},
				},
			)
			errorsByAttempt <- err
		}()
	}
	calls.Wait()
	close(errorsByAttempt)
	succeeded := 0
	rejected := 0
	for err := range errorsByAttempt {
		if err == nil {
			succeeded++
			continue
		}
		var typed *subagent.Error
		if !errors.As(err, &typed) ||
			typed.Code != subagent.ErrorDuplicateBoundBinding {
			t.Fatalf("concurrent Bind error = %v", err)
		}
		rejected++
	}
	if succeeded != 1 || rejected != attempts-1 {
		t.Fatalf(
			"concurrent Bind results = %d succeeded, %d rejected",
			succeeded,
			rejected,
		)
	}
}

func bindConfigChild(
	t *testing.T,
	fixture boundConfigFixture,
	title string,
	identifier session.SessionID,
) session.SessionID {
	t.Helper()
	_, err := fixture.owner.Bind(
		context.Background(),
		subagent.BindCommand{
			Parent:           fixture.parentAgent,
			RequestedChildID: &identifier,
			SeedBuilder:      "spawn",
			Title:            title,
			InitialPrompt: []agentmessage.ContentBlock{
				agentmessage.NewTextBlock("start"),
			},
			Config: subagent.BoundConfigInput{
				Enabled: true,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return identifier
}

func boundView(
	t *testing.T,
	fixture boundConfigFixture,
) subagentprojection.Bound {
	t.Helper()
	snapshot, err := fixture.projections.Snapshot(
		fixture.parentAgent.SessionValue(),
	)
	if err != nil {
		t.Fatal(err)
	}
	view, found, err := subagentprojection.ReadBound(snapshot.Values)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("Bound projection is absent")
	}
	return view
}
