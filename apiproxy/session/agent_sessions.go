// Package sessionapi contains the Session-specific application state used by
// the API Proxy. It coordinates Agent activation without owning wire DTOs or
// duplicating the session domain model.
package sessionapi

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentdefaultmodel"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	sesspersist "github.com/gorenx/goren/session/persistence"
)

// DirectoryProvisioner owns the filesystem side effect required before an
// API-created Session records its project directory.
type DirectoryProvisioner interface {
	EnsureDirectory(string) error
}

// DirectoryProvisionerFunc adapts a function at the composition boundary.
type DirectoryProvisionerFunc func(string) error

func (operation DirectoryProvisionerFunc) EnsureDirectory(path string) error {
	return operation(path)
}

// AgentSessionDependencies are domain capabilities used to activate ordinary
// Agents for API-visible Sessions.
type AgentSessionDependencies struct {
	Agents      agent.Registry
	Sessions    session.Store
	Persistence sesspersist.Persistence
	Defaults    agentdefaultmodel.DefaultModel
	Directories DirectoryProvisioner
}

type creationResult struct {
	done    chan struct{}
	subject agent.Agent
	err     error
}

type installedSelection struct {
	subject agent.Agent
	ref     *agent.ModelSelectionRef
}

// AgentSessions serializes activation per Session and owns the model-selection
// reference installed into each API-addressable ordinary Agent.
type AgentSessions struct {
	sourceScope *plugin.Scope
	agents      agent.Registry
	sessions    session.Store
	persistence sesspersist.Persistence
	defaults    agentdefaultmodel.DefaultModel
	directories DirectoryProvisioner

	creationMutex sync.Mutex
	creations     map[session.SessionID]*creationResult
	selectionMu   sync.Mutex
	selections    map[session.SessionID]installedSelection
}

// NewAgentSessions creates the state owner and installs selection cleanup on
// the source Scope.
func NewAgentSessions(
	sourceScope *plugin.Scope,
	ports AgentSessionDependencies,
) (*AgentSessions, error) {
	if sourceScope == nil {
		return nil, errors.New("apiproxy/session: Scope is required")
	}
	if ports.Agents == nil || ports.Sessions == nil || ports.Persistence == nil ||
		ports.Defaults == nil || ports.Directories == nil {
		return nil, errors.New("apiproxy/session: Agent Session dependencies are incomplete")
	}
	owner := &AgentSessions{
		sourceScope: sourceScope,
		agents:      ports.Agents, sessions: ports.Sessions, persistence: ports.Persistence,
		defaults: ports.Defaults, directories: ports.Directories,
		creations:  make(map[session.SessionID]*creationResult),
		selections: make(map[session.SessionID]installedSelection),
	}
	if _, err := agent.OnDisposed(sourceScope, owner.observeAgentDisposed); err != nil {
		return nil, err
	}
	return owner, nil
}

// Ensure returns an existing matching ordinary Agent, resumes a persisted
// Session, or creates a missing Session when createMissing is true.
func (owner *AgentSessions) Ensure(
	requestContext context.Context,
	identifier session.SessionID,
	workingDirectory string,
	requestedAgentPreset *string,
	createMissing bool,
	knownInspection *sesspersist.Inspection,
) (agent.Agent, error) {
	owner.creationMutex.Lock()
	if pending := owner.creations[identifier]; pending != nil {
		owner.creationMutex.Unlock()
		select {
		case <-pending.done:
			if pending.err != nil {
				return nil, pending.err
			}
			return owner.assertAdoption(pending.subject, workingDirectory, requestedAgentPreset)
		case <-requestContext.Done():
			return nil, requestContext.Err()
		}
	}
	pending := &creationResult{done: make(chan struct{})}
	owner.creations[identifier] = pending
	owner.creationMutex.Unlock()

	pending.subject, pending.err = owner.createOrAdopt(
		requestContext, identifier, workingDirectory, createMissing, knownInspection,
	)
	owner.creationMutex.Lock()
	delete(owner.creations, identifier)
	close(pending.done)
	owner.creationMutex.Unlock()
	if pending.err != nil {
		return nil, pending.err
	}
	return owner.assertAdoption(pending.subject, workingDirectory, requestedAgentPreset)
}

// Ordinary resolves an API-addressable ordinary Agent, activating a persisted
// Session when necessary.
func (owner *AgentSessions) Ordinary(
	requestContext context.Context,
	identifier session.SessionID,
) (agent.Agent, error) {
	subject, found := owner.agents.Get(identifier)
	if !found {
		loaded, err := owner.persistence.Inspect(requestContext, identifier)
		if err != nil {
			return nil, err
		}
		workingDirectory := ""
		if loaded.Header.CWD != nil {
			workingDirectory = *loaded.Header.CWD
		}
		subject, err = owner.Ensure(
			requestContext, loaded.Header.ID, workingDirectory, loaded.Header.AgentPreset, false, &loaded,
		)
		if err != nil {
			return nil, err
		}
	}
	header := subject.SessionValue().Header()
	if header.Origin == session.OriginSubagent || owner.hasLiveParent(subject, header) {
		return nil, &SubagentOwnershipError{identifier: identifier}
	}
	return subject, nil
}

// Selection returns the stateful model selection installed in an Agent Scope.
func (owner *AgentSessions) Selection(subject agent.Agent) (*agent.ModelSelectionRef, error) {
	identifier := subject.ID()
	owner.selectionMu.Lock()
	defer owner.selectionMu.Unlock()
	if installed, found := owner.selections[identifier]; found && installed.subject == subject {
		return installed.ref, nil
	}
	selectionRef := agent.NewModelSelectionRef(func() (agent.ModelSelection, bool, error) {
		return owner.loggedOrDefaultSelection(subject.SessionValue())
	})
	if _, err := agent.InstallModelSelection(subject.ScopeValue(), selectionRef); err != nil {
		return nil, err
	}
	owner.selections[identifier] = installedSelection{subject: subject, ref: selectionRef}
	return selectionRef, nil
}

func (owner *AgentSessions) createOrAdopt(
	requestContext context.Context,
	identifier session.SessionID,
	workingDirectory string,
	createMissing bool,
	knownInspection *sesspersist.Inspection,
) (agent.Agent, error) {
	if subject, found := owner.agents.Get(identifier); found {
		return subject, nil
	}
	if conversation, found := owner.sessions.Get(identifier); found {
		return nil, fmt.Errorf("session %q is attached without a live Agent (cwd %v)", identifier, conversation.Header().CWD)
	}
	loaded := sesspersist.Inspection{}
	var inspectErr error
	if knownInspection == nil {
		loaded, inspectErr = owner.persistence.Inspect(requestContext, identifier)
	} else {
		loaded = *knownInspection
	}
	if inspectErr == nil {
		return owner.resumeCold(requestContext, loaded)
	}
	var missing *sesspersist.NotFoundError
	if !errors.As(inspectErr, &missing) {
		return nil, inspectErr
	}
	if !createMissing {
		return nil, missing
	}
	if !filepath.IsAbs(workingDirectory) {
		return nil, fmt.Errorf("project directory %q must be absolute", workingDirectory)
	}
	if err := owner.directories.EnsureDirectory(workingDirectory); err != nil {
		return nil, fmt.Errorf("failed to ensure project directory %q: %w", workingDirectory, err)
	}
	defaultSelection := owner.defaults.CurrentSelection()
	selectionRef := agent.NewModelSelectionRef(func() (agent.ModelSelection, bool, error) {
		conversation, found := owner.sessions.Get(identifier)
		if found {
			return owner.loggedOrDefaultSelection(conversation)
		}
		selected := owner.defaults.CurrentSelection()
		return selected, selected.Provider != "" && selected.Model != "", nil
	})
	setup := agent.SetupFunc(func(_ context.Context, agentScope *plugin.Scope) (agent.SetupCommit, error) {
		_, err := agent.InstallModelSelection(agentScope, selectionRef)
		return nil, err
	})
	handle, err := owner.agents.Create(requestContext, owner.sourceScope, agent.CreateOptions{
		SessionID: identifier,
		Metadata:  session.Metadata{CWD: &workingDirectory},
		AgentOptions: agent.Options{
			Provider: defaultSelection.Provider, Model: defaultSelection.Model,
		},
		Setup: setup,
	})
	if err != nil {
		if subject, found := owner.agents.Get(identifier); found {
			return subject, nil
		}
		return nil, err
	}
	owner.selectionMu.Lock()
	owner.selections[identifier] = installedSelection{subject: handle.Subject, ref: selectionRef}
	owner.selectionMu.Unlock()
	return handle.Subject, nil
}

func (owner *AgentSessions) resumeCold(
	requestContext context.Context,
	loaded sesspersist.Inspection,
) (agent.Agent, error) {
	identifier := loaded.Header.ID
	if subject, found := owner.agents.Get(identifier); found {
		return subject, nil
	}
	transient, err := session.New(identifier, session.CreateOptions{
		Seed: loaded.Events, Metadata: metadataFromHeader(loaded.Header),
	})
	if err != nil {
		return nil, err
	}
	selected, selectedFound, err := owner.loggedOrDefaultSelection(transient)
	if err != nil {
		return nil, err
	}
	loopOptions := agent.Options{}
	if selectedFound {
		loopOptions.Provider = selected.Provider
		loopOptions.Model = selected.Model
	}
	if requestHeader, found, headerErr := transient.RequestHeaderValue(); headerErr != nil {
		return nil, headerErr
	} else if found && requestHeader.Config.MaxTokens != nil {
		tokenLimit := *requestHeader.Config.MaxTokens
		loopOptions.MaxTokens = &tokenLimit
	}
	selectionRef := agent.NewModelSelectionRef(func() (agent.ModelSelection, bool, error) {
		conversation, found := owner.sessions.Get(identifier)
		if !found {
			return agent.ModelSelection{}, false, nil
		}
		return owner.loggedOrDefaultSelection(conversation)
	})
	setup := agent.SetupFunc(func(_ context.Context, agentScope *plugin.Scope) (agent.SetupCommit, error) {
		_, err := agent.InstallModelSelection(agentScope, selectionRef)
		return nil, err
	})
	handle, err := owner.agents.Resume(requestContext, owner.sourceScope, agent.ResumeOptions{
		SessionID: identifier, AgentOptions: loopOptions, Setup: setup,
	})
	if err != nil {
		if subject, found := owner.agents.Get(identifier); found {
			return subject, nil
		}
		return nil, err
	}
	owner.selectionMu.Lock()
	owner.selections[identifier] = installedSelection{subject: handle.Subject, ref: selectionRef}
	owner.selectionMu.Unlock()
	return handle.Subject, nil
}

func (owner *AgentSessions) assertAdoption(
	subject agent.Agent,
	workingDirectory string,
	requestedAgentPreset *string,
) (agent.Agent, error) {
	header := subject.SessionValue().Header()
	if header.Origin == session.OriginSubagent || owner.hasLiveParent(subject, header) {
		return nil, &SubagentOwnershipError{identifier: subject.ID()}
	}
	if requestedAgentPreset != nil && (header.AgentPreset == nil || *header.AgentPreset != *requestedAgentPreset) {
		return nil, &PresetConflictError{
			identifier: subject.ID(), requested: *requestedAgentPreset,
			existing: cloneStringPointer(header.AgentPreset),
		}
	}
	if header.CWD == nil || *header.CWD != workingDirectory {
		return nil, &CWDConflictError{
			identifier: subject.ID(), requested: workingDirectory, existing: cloneStringPointer(header.CWD),
		}
	}
	return subject, nil
}

func (owner *AgentSessions) hasLiveParent(subject agent.Agent, header session.Header) bool {
	if header.ParentSession == nil {
		return false
	}
	parent, found := owner.agents.Get(*header.ParentSession)
	return found && owner.agents.IsOwnedBy(subject.ID(), parent)
}

func (owner *AgentSessions) loggedOrDefaultSelection(
	conversation *session.Session,
) (agent.ModelSelection, bool, error) {
	header, found, err := conversation.RequestHeaderValue()
	if err != nil {
		return agent.ModelSelection{}, false, err
	}
	if found {
		return agent.ModelSelection{
			Provider: header.Config.Provider, Model: header.Config.Model,
			ReasoningEffort: header.Config.ReasoningEffort,
		}, true, nil
	}
	selected := owner.defaults.CurrentSelection()
	if selected.Provider != "" && selected.Model != "" {
		return selected, true, nil
	}
	return agent.ModelSelection{}, false, nil
}

func (owner *AgentSessions) observeAgentDisposed(_ context.Context, subject agent.Agent) error {
	owner.selectionMu.Lock()
	if installed, found := owner.selections[subject.ID()]; found && installed.subject == subject {
		delete(owner.selections, subject.ID())
	}
	owner.selectionMu.Unlock()
	return nil
}

func metadataFromHeader(header session.Header) session.Metadata {
	createdAt := header.CreatedAt
	return session.Metadata{
		CreatedAt: &createdAt, CWD: cloneStringPointer(header.CWD),
		ParentSession: cloneSessionIDPointer(header.ParentSession), SeedLength: cloneInt64Pointer(header.SeedLength),
		Origin: header.Origin, DelegationDepth: cloneInt64Pointer(header.DelegationDepth),
		AgentPreset: cloneStringPointer(header.AgentPreset),
	}
}

func cloneStringPointer(source *string) *string {
	if source == nil {
		return nil
	}
	copyValue := *source
	return &copyValue
}

func cloneSessionIDPointer(source *session.SessionID) *session.SessionID {
	if source == nil {
		return nil
	}
	copyValue := *source
	return &copyValue
}

func cloneInt64Pointer(source *int64) *int64 {
	if source == nil {
		return nil
	}
	copyValue := *source
	return &copyValue
}
