package sessionapi

import (
	"context"
	"errors"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentdefaultmodel"
	"github.com/gorenx/goren/session"
	sesspersist "github.com/gorenx/goren/session/persistence"
)

type agentRegistryStub struct{}

func (agentRegistryStub) Get(session.SessionID) (agent.Agent, bool) {
	return nil, false
}

func (agentRegistryStub) Contains(agent.Agent) bool {
	return false
}

func (agentRegistryStub) List() []agent.Agent {
	return nil
}

type constructorRecord struct {
	createCalls int
	resumeCalls int
}

func (record *constructorRecord) Create(
	context.Context,
	agent.CreateOptions,
) (agent.Handle, error) {
	record.createCalls++
	return agent.Handle{}, errors.New("test: unexpected Create")
}

func (record *constructorRecord) Resume(
	context.Context,
	agent.ResumeOptions,
) (agent.Handle, error) {
	record.resumeCalls++
	return agent.Handle{}, errors.New("test: unexpected Resume")
}

type scopeProvisioningStub struct{}

func (scopeProvisioningStub) ApplySetup(
	context.Context,
	agent.Agent,
	agent.Setup,
) (agent.ScopeResources, error) {
	return scopeResourcesStub{}, nil
}

type scopeResourcesStub struct{}

func (scopeResourcesStub) Close(context.Context) error { return nil }

type liveStoreStub struct{}

func (liveStoreStub) Create(
	context.Context,
	*session.SessionID,
	session.CreateOptions,
) (session.Handle, error) {
	return nil, errors.New("test: unexpected Session Create")
}

func (liveStoreStub) Prepare(
	*session.SessionID,
	session.CreateOptions,
) (session.Context, error) {
	return nil, errors.New("test: unexpected Session Prepare")
}

func (liveStoreStub) Enter(session.Context) (session.Handle, error) {
	return nil, errors.New("test: unexpected Session Enter")
}

func (liveStoreStub) Announce(context.Context, session.Context) error {
	return errors.New("test: unexpected Session Announce")
}

func (liveStoreStub) Flush(context.Context, session.Context) error {
	return errors.New("test: unexpected Session Flush")
}

func (liveStoreStub) Get(session.SessionID) (session.Context, bool) {
	return nil, false
}

func (liveStoreStub) List() []session.Context {
	return nil
}

type persistenceStub struct{}

func (persistenceStub) Locate(session.Header) (sesspersist.Location, bool) {
	return sesspersist.Location{}, false
}

func (persistenceStub) SupportsRawArtifacts() bool {
	return false
}

func (persistenceStub) ReadRaw(
	context.Context,
	session.SessionID,
) (sesspersist.RawArtifact, error) {
	return sesspersist.RawArtifact{}, errors.New("test: unexpected ReadRaw")
}

func (persistenceStub) Create(context.Context, session.Header) error {
	return errors.New("test: unexpected persistence Create")
}

func (persistenceStub) Append(
	context.Context,
	session.SessionID,
	[]session.Event,
) error {
	return errors.New("test: unexpected Append")
}

func (persistenceStub) Prepare(
	context.Context,
	session.SessionID,
) (*session.Preparation, error) {
	return nil, errors.New("test: unexpected persistence Prepare")
}

func (persistenceStub) Load(
	context.Context,
	session.SessionID,
) (sesspersist.Inspection, error) {
	return sesspersist.Inspection{}, errors.New("test: unexpected Load")
}

func (persistenceStub) Inspect(
	context.Context,
	session.SessionID,
) (sesspersist.Inspection, error) {
	return sesspersist.Inspection{}, errors.New("test: unexpected Inspect")
}

func (persistenceStub) ReadEventsFrom(
	context.Context,
	session.SessionID,
	sesspersist.EventContinuation,
) (sesspersist.EventSegment, error) {
	return sesspersist.EventSegment{}, errors.New("test: unexpected ReadEventsFrom")
}

func (persistenceStub) ReadEventsBefore(
	context.Context,
	session.SessionID,
	sesspersist.EventPage,
) (sesspersist.EventWindow, error) {
	return sesspersist.EventWindow{}, errors.New("test: unexpected ReadEventsBefore")
}

func (persistenceStub) List(
	context.Context,
	sesspersist.SessionPage,
) (sesspersist.HeaderPage, error) {
	return sesspersist.HeaderPage{}, errors.New("test: unexpected List")
}

func (persistenceStub) ListSnapshots(
	context.Context,
	sesspersist.SessionPage,
) (sesspersist.SnapshotPage, error) {
	return sesspersist.SnapshotPage{}, errors.New("test: unexpected ListSnapshots")
}

type defaultModelStub struct{}

func (defaultModelStub) CurrentSelection() agent.ModelSelection {
	return agent.ModelSelection{
		Provider: "test",
		Model:    "test",
	}
}

func (defaultModelStub) SaveSelection(
	context.Context,
	agent.ModelSelection,
) error {
	return nil
}

type directoryProvisionerStub struct{}

func (directoryProvisionerStub) EnsureDirectory(string) error {
	return nil
}

func TestEnsureRejectsPersistedSubagentBeforeResume(t *testing.T) {
	t.Parallel()
	constructor := &constructorRecord{}
	owner, err := NewAgentSessions(
		AgentSessionDependencies{
			Agents:      agentRegistryStub{},
			Constructor: constructor,
			Scopes:      scopeProvisioningStub{},
			Sessions:    liveStoreStub{},
			Persistence: persistenceStub{},
			Defaults:    defaultModelStub{},
			Directories: directoryProvisionerStub{},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	identifier := session.SessionID("persisted-subagent")
	_, err = owner.Ensure(
		context.Background(),
		identifier,
		"/workspace",
		nil,
		false,
		&sesspersist.Inspection{
			Header: session.Header{
				ID:     identifier,
				Origin: session.OriginSubagent,
			},
		},
	)
	var ownershipErr *SubagentOwnershipError
	if !errors.As(err, &ownershipErr) {
		t.Fatalf("Ensure error = %v, want SubagentOwnershipError", err)
	}
	if constructor.createCalls != 0 || constructor.resumeCalls != 0 {
		t.Fatalf(
			"Constructor calls after durable Subagent classification: create=%d resume=%d",
			constructor.createCalls,
			constructor.resumeCalls,
		)
	}
}

var _ agent.Registry = agentRegistryStub{}
var _ agent.Constructor = (*constructorRecord)(nil)
var _ agent.ScopeSetup = scopeProvisioningStub{}
var _ session.LiveStore = liveStoreStub{}
var _ sesspersist.Persistence = persistenceStub{}
var _ agentdefaultmodel.DefaultModel = defaultModelStub{}
var _ DirectoryProvisioner = directoryProvisionerStub{}
