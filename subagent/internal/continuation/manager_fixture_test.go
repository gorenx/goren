package continuation

import (
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/subagent"
)

type managerFixture struct {
	manager     *Manager
	parent      *agentRecord
	agents      *registryRecord
	sessions    *sessionRecord
	persistence *persistenceRecord
	lifecycle   *lifecycleRecord
}

func newManagerFixture(t *testing.T) *managerFixture {
	t.Helper()
	parentSession, sessionErr := session.New("parent", session.CreateOptions{})
	if sessionErr != nil {
		t.Fatal(sessionErr)
	}
	parentAgent := &agentRecord{
		identifier:   "parent",
		conversation: parentSession,
		options: agent.Options{
			Provider: "deepseek",
			Model:    "chat",
		},
		status: agent.StatusRunning,
		idle:   make(chan struct{}),
	}
	liveSessions := &sessionRecord{
		entries: map[session.SessionID]*session.Session{
			parentAgent.ID(): parentSession,
		},
	}
	storedSessions := &persistenceRecord{
		inspections: make(map[session.SessionID]persistence.Inspection),
	}
	agentRegistry := &registryRecord{
		agents: map[session.SessionID]*agentRecord{
			parentAgent.ID(): parentAgent,
		},
		sessions: liveSessions,
		stored:   storedSessions,
	}
	lifecycleFacts := &lifecycleRecord{}
	agentCustody, custodyErr := agent.NewCustody(parentAgent)
	if custodyErr != nil {
		t.Fatal(custodyErr)
	}
	owner, managerErr := New(Dependencies{
		Agents:      agentRegistry,
		Custody:     agentCustody,
		Sessions:    liveSessions,
		Persistence: storedSessions,
		Providers: providerSource{
			candidate: providerRecord{},
		},
		Lifecycle:    lifecycleFacts,
		ScopeBuilder: scopeBuilderStub{},
	})
	if managerErr != nil {
		t.Fatal(managerErr)
	}
	return &managerFixture{
		manager:     owner,
		parent:      parentAgent,
		agents:      agentRegistry,
		sessions:    liveSessions,
		persistence: storedSessions,
		lifecycle:   lifecycleFacts,
	}
}

func (fixture *managerFixture) storeContinuableChild(
	t *testing.T,
	childID session.SessionID,
) {
	t.Helper()
	descriptor := subagent.ContinuableDescriptor{
		Provider:      "spawn",
		Label:         "resume",
		AgentProvider: stringPointer("deepseek"),
		AgentModel:    stringPointer("chat"),
	}
	seed, seedErr := descriptorSeed(childID, nil, descriptor)
	if seedErr != nil {
		t.Fatal(seedErr)
	}
	depth := int64(1)
	storedSession, createErr := session.New(
		childID,
		session.CreateOptions{
			Seed: seed,
			Metadata: session.Metadata{
				ParentSession:   sessionIDReference(fixture.parent.ID()),
				Origin:          session.OriginSubagent,
				DelegationDepth: &depth,
			},
		},
	)
	if createErr != nil {
		t.Fatal(createErr)
	}
	fixture.persistence.inspections[childID] = persistence.Inspection{
		Header: storedSession.Header(),
		Events: storedSession.Events(),
	}
}
