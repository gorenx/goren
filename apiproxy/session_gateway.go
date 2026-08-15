package apiproxy

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentdefaultmodel"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	sessionpersistence "github.com/gorenx/goren/session/persistence"
	sessionprojection "github.com/gorenx/goren/session/projection"
	sessiontitle "github.com/gorenx/goren/session/title"
	"github.com/gorenx/goren/workspace"
)

const defaultHistoryMessages int64 = 50

var ianaTimeZonePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+.-]*(?:/[A-Za-z0-9_+.-]+)+$`)

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

// SessionGatewayDependencies are existing domain capabilities consumed by
// the Host API adapter; none of them depend on wire types.
type SessionGatewayDependencies struct {
	Agents      agent.Registry
	Sessions    session.Store
	Persistence sessionpersistence.Persistence
	LLM         llm.LlmRuntime
	Defaults    agentdefaultmodel.DefaultModel
	Projections sessionprojection.Registry
	Titles      sessiontitle.TitleService
	Workspaces  workspace.Registry
	Directories DirectoryProvisioner
}

// SessionGatewayOptions are process values and injectable identity sources.
type SessionGatewayOptions struct {
	WorkingDirectory string
	NewSessionID     func() (session.SessionID, error)
	NewRPCID         func() (connection.RPCID, error)
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

// SessionGateway implements the included session.* use cases and both live
// frame feeds without moving Agent or Session rules into the transport.
type SessionGateway struct {
	sourceScope *plugin.Scope
	agents      agent.Registry
	sessions    session.Store
	persistence sessionpersistence.Persistence
	models      llm.LlmRuntime
	defaults    agentdefaultmodel.DefaultModel
	projections sessionprojection.Registry
	titles      sessiontitle.TitleService
	workspaces  workspace.Registry
	directories DirectoryProvisioner
	workingDir  string
	newSession  func() (session.SessionID, error)
	newRPC      func() (connection.RPCID, error)

	creationMutex  sync.Mutex
	creations      map[session.SessionID]*creationResult
	selectionMutex sync.Mutex
	selections     map[session.SessionID]installedSelection

	hub *sessionFrameHub
}

// NewSessionGateway installs source-owned lifecycle observers and returns the
// typed API implementation plus its stream handlers.
func NewSessionGateway(
	requestContext context.Context,
	sourceScope *plugin.Scope,
	ports SessionGatewayDependencies,
	settings SessionGatewayOptions,
) (*SessionGateway, error) {
	if requestContext == nil || sourceScope == nil {
		return nil, errors.New("apiproxy: Session Gateway Context and Scope are required")
	}
	if ports.Agents == nil || ports.Sessions == nil || ports.Persistence == nil || ports.LLM == nil || ports.Defaults == nil ||
		ports.Projections == nil || ports.Titles == nil || ports.Workspaces == nil || ports.Directories == nil {
		return nil, errors.New("apiproxy: Session Gateway dependencies are incomplete")
	}
	if settings.WorkingDirectory == "" {
		return nil, errors.New("apiproxy: Session Gateway working directory is empty")
	}
	newSession := settings.NewSessionID
	if newSession == nil {
		newSession = mintSessionID
	}
	newRPC := settings.NewRPCID
	if newRPC == nil {
		newRPC = mintFrameRPCID
	}
	owner := &SessionGateway{
		sourceScope: sourceScope, agents: ports.Agents, sessions: ports.Sessions, persistence: ports.Persistence,
		models: ports.LLM, defaults: ports.Defaults, projections: ports.Projections, titles: ports.Titles,
		workspaces:  ports.Workspaces,
		directories: ports.Directories,
		workingDir:  settings.WorkingDirectory, newSession: newSession, newRPC: newRPC,
		creations:  make(map[session.SessionID]*creationResult),
		selections: make(map[session.SessionID]installedSelection),
	}
	owner.hub = newSessionFrameHub(owner.newRPC)
	if err := owner.installObservers(requestContext); err != nil {
		return nil, err
	}
	if _, err := plugin.Own(sourceScope, "apiProxy.sessionGateway()", owner.close); err != nil {
		return nil, err
	}
	return owner, nil
}

func (owner *SessionGateway) installObservers(requestContext context.Context) error {
	if _, err := session.OnEvent(owner.sourceScope, owner.observeSessionEvent); err != nil {
		return err
	}
	if _, err := session.OnCreated(owner.sourceScope, owner.observeSessionCreated); err != nil {
		return err
	}
	if _, err := session.OnDisposed(owner.sourceScope, owner.observeSessionDisposed); err != nil {
		return err
	}
	if _, err := agent.OnStatus(owner.sourceScope, owner.observeAgentStatus); err != nil {
		return err
	}
	if _, err := agent.OnError(owner.sourceScope, owner.observeAgentError); err != nil {
		return err
	}
	if _, err := agent.OnDisposed(owner.sourceScope, owner.observeAgentDisposed); err != nil {
		return err
	}
	if _, err := owner.projections.OnChanged(owner.sourceScope, sessionprojection.ChangeListenerFunc(owner.observeProjectionChange)); err != nil {
		return err
	}
	return requestContext.Err()
}

func (owner *SessionGateway) close(context.Context) error {
	owner.hub.close()
	return nil
}

// Mux streams per-session baselines and later committed changes.
func (owner *SessionGateway) Mux(requestContext context.Context, emit func(StreamRequest[MuxFrame]) error) error {
	return owner.hub.openMux(requestContext, owner.sessions.List(), emit)
}

// Host streams host-level edges; reconnect baselines remain session.list's responsibility.
func (owner *SessionGateway) Host(requestContext context.Context, emit func(StreamRequest[HostFrame]) error) error {
	return owner.hub.openHost(requestContext, emit)
}

// PublishHostFrame lets sibling API domains publish committed Host-level
// changes through the one connection-owned downlink hub.
func (owner *SessionGateway) PublishHostFrame(payload HostFrame) error {
	if payload == nil {
		return errors.New("apiproxy: Host frame is nil")
	}
	return owner.hub.hostFrame(payload)
}

// List returns the current attached Session baseline, newest human activity first.
func (owner *SessionGateway) List(requestContext context.Context, _ Request[SessionListRequest]) (Outcome[SessionListValue], error) {
	items := make([]SessionSummary, 0)
	liveIDs := make(map[session.SessionID]struct{})
	for _, conversation := range owner.sessions.List() {
		header := conversation.Header()
		if header.CWD == nil {
			continue
		}
		events := conversation.Events()
		liveIDs[header.ID] = struct{}{}
		projectionSnapshot, projectionErr := owner.projections.Snapshot(conversation)
		if projectionErr != nil {
			return Outcome[SessionListValue]{}, projectionErr
		}
		running := false
		if subject, found := owner.agents.Get(header.ID); found {
			running = subject.StatusValue() == agent.StatusRunning
		}
		items = append(items, summarizeSession(header, events, running, projectionSnapshot))
	}
	storedHeaders, err := owner.persistence.List(requestContext)
	if err != nil {
		return Outcome[SessionListValue]{}, err
	}
	for _, header := range storedHeaders {
		if _, live := liveIDs[header.ID]; live || header.CWD == nil {
			continue
		}
		loaded, err := owner.persistence.Inspect(requestContext, header.ID)
		if err != nil {
			return Outcome[SessionListValue]{}, err
		}
		restored, err := owner.projections.Restore(sessionprojection.Checkpoint{}, loaded.Events, 0)
		if err != nil {
			return Outcome[SessionListValue]{}, err
		}
		items = append(items, summarizeSession(loaded.Header, loaded.Events, false, restored.Snapshot))
	}
	sort.SliceStable(items, func(leftIndex int, rightIndex int) bool {
		return items[leftIndex].UpdatedAt > items[rightIndex].UpdatedAt
	})
	if items == nil {
		items = []SessionSummary{}
	}
	return OK(SessionListValue{Items: items}), nil
}

// Rename delegates normalization, pinning, and event append to Session Title.
func (owner *SessionGateway) Rename(requestContext context.Context, call Request[SessionRenameRequest]) (Outcome[SessionRenameValue], error) {
	subject, refused := owner.ordinaryAgent(requestContext, call.Payload.SessionID)
	if refused != nil {
		return Fail[SessionRenameValue](*refused), nil
	}
	accepted, err := owner.titles.Rename(subject.SessionValue(), call.Payload.Title)
	if err != nil {
		var invalid *sessiontitle.SessionTitleInvalidError
		if errors.As(err, &invalid) {
			return Fail[SessionRenameValue](newRPCError(
				connection.ErrorTitleInvalid, invalid.Error(), struct {
					SessionID SessionID `json:"sessionId"`
				}{SessionID: call.Payload.SessionID},
			)), nil
		}
		return Fail[SessionRenameValue](newRPCError(
			connection.ErrorInternal,
			fmt.Sprintf("failed to rename session %q: %v", call.Payload.SessionID, err),
			struct{}{},
		)), nil
	}
	return OK(SessionRenameValue{Title: accepted.Title, Seq: accepted.EventSeq}), nil
}

// Create publishes a fresh ordinary Agent or idempotently adopts the same id and cwd.
func (owner *SessionGateway) Create(requestContext context.Context, call Request[SessionCreateRequest]) (Outcome[SessionCreateValue], error) {
	var workspaceSubject workspace.Workspace
	var workspaceSnapshot workspace.WorkspaceState
	if call.Payload.WorkspaceID != nil {
		resolved, found := owner.workspaces.Get(workspace.ID(*call.Payload.WorkspaceID))
		if !found {
			return workspaceNotFound[SessionCreateValue](*call.Payload.WorkspaceID), nil
		}
		workspaceSnapshot = resolved.Snapshot()
		if workspaceSnapshot.ID == "" {
			return workspaceNotFound[SessionCreateValue](*call.Payload.WorkspaceID), nil
		}
		workspaceSubject = resolved
	}
	identifier := session.SessionID("")
	if call.Payload.SessionID == nil {
		generated, err := owner.newSession()
		if err != nil {
			return Outcome[SessionCreateValue]{}, err
		}
		identifier = generated
	} else {
		identifier = session.SessionID(*call.Payload.SessionID)
	}
	workingDirectory := owner.workingDir
	if workspaceSubject != nil {
		workingDirectory = workspaceSnapshot.Path
	} else if call.Payload.CWD != nil {
		workingDirectory = *call.Payload.CWD
	}
	subject, err := owner.ensureSession(requestContext, identifier, workingDirectory, call.Payload.AgentPreset, true, nil)
	if err != nil {
		var ownership *subagentSessionOwnership
		if errors.As(err, &ownership) {
			return Fail[SessionCreateValue](subagentOwnershipError(SessionID(ownership.identifier))), nil
		}
		var presetConflict *sessionPresetConflict
		if errors.As(err, &presetConflict) {
			return Fail[SessionCreateValue](newRPCError(
				connection.ErrorAgentPresetConflict, presetConflict.Error(), struct {
					SessionID       SessionID `json:"sessionId"`
					RequestedPreset string    `json:"requestedPreset"`
					ExistingPreset  *string   `json:"existingPreset,omitempty"`
				}{
					SessionID: SessionID(presetConflict.identifier), RequestedPreset: presetConflict.requested,
					ExistingPreset: cloneStringPointer(presetConflict.existing),
				},
			)), nil
		}
		var conflict *sessionCWDConflict
		if errors.As(err, &conflict) {
			return Fail[SessionCreateValue](newRPCError(
				connection.ErrorSessionConflict, conflict.Error(), struct {
					SessionID    SessionID `json:"sessionId"`
					RequestedCWD string    `json:"requestedCwd"`
					ExistingCWD  *string   `json:"existingCwd,omitempty"`
				}{SessionID: SessionID(identifier), RequestedCWD: workingDirectory, ExistingCWD: conflict.existing},
			)), nil
		}
		return Fail[SessionCreateValue](newRPCError(
			connection.ErrorInternal,
			fmt.Sprintf("failed to create session %q: %v", identifier, err),
			struct{}{},
		)), nil
	}
	if workspaceSubject != nil {
		if err := workspaceSubject.AttachSession(requestContext, subject.SessionValue().ID()); err != nil {
			return Fail[SessionCreateValue](newRPCError(
				connection.ErrorWorkspaceAttachFailed,
				fmt.Sprintf(
					"session %q was created but could not attach to workspace %q: %v",
					subject.SessionValue().ID(), workspaceSnapshot.ID, err,
				),
				struct {
					SessionID   SessionID   `json:"sessionId"`
					WorkspaceID WorkspaceID `json:"workspaceId"`
				}{
					SessionID:   SessionID(subject.SessionValue().ID()),
					WorkspaceID: WorkspaceID(workspaceSnapshot.ID),
				},
			)), nil
		}
	}
	header := subject.SessionValue().Header()
	return OK(SessionCreateValue{
		SessionID: SessionID(identifier), AgentPreset: cloneStringPointer(header.AgentPreset),
	}), nil
}

// History reads without activating an Agent and paginates by append-origin messages.
func (owner *SessionGateway) History(requestContext context.Context, call Request[SessionHistoryRequest]) (Outcome[SessionHistoryValue], error) {
	identifier := session.SessionID(call.Payload.SessionID)
	conversation, found := owner.sessions.Get(identifier)
	var events []session.Event
	if found {
		events = conversation.Events()
	} else {
		loaded, err := owner.persistence.Inspect(requestContext, identifier)
		if err != nil {
			var missing *sessionpersistence.NotFoundError
			if errors.As(err, &missing) {
				return Fail[SessionHistoryValue](sessionNotFoundError(call.Payload.SessionID)), nil
			}
			return Outcome[SessionHistoryValue]{}, err
		}
		events = loaded.Events
	}
	maxMessages := defaultHistoryMessages
	if call.Payload.MaxMessages != nil {
		maxMessages = *call.Payload.MaxMessages
	}
	page, hasMore, err := historyPage(events, call.Payload.BeforeSeq, maxMessages)
	if err != nil {
		return Outcome[SessionHistoryValue]{}, err
	}
	value := SessionHistoryValue{Events: page, HasMore: hasMore}
	if call.Payload.BeforeSeq == nil {
		restored, restoreErr := owner.projections.Restore(sessionprojection.Checkpoint{}, events, 0)
		if restoreErr != nil {
			return Outcome[SessionHistoryValue]{}, restoreErr
		}
		value.Projections = projectionBlock(restored.Snapshot)
	}
	return OK(value), nil
}

// Models returns one session's current selection plus independently loaded provider groups.
func (owner *SessionGateway) Models(requestContext context.Context, call Request[SessionModelsRequest]) (Outcome[SessionModelsValue], error) {
	subject, refused := owner.ordinaryAgent(requestContext, call.Payload.SessionID)
	if refused != nil {
		return Fail[SessionModelsValue](*refused), nil
	}
	selection, err := owner.selectionFor(subject)
	if err != nil {
		return Outcome[SessionModelsValue]{}, err
	}
	current, _, err := selection.Current()
	if err != nil {
		return Outcome[SessionModelsValue]{}, err
	}
	groups, failures := owner.modelCatalog(requestContext)
	routable := slices.ContainsFunc(owner.models.ListProviders(), func(provider llm.ProviderInfo) bool {
		return provider.ID == current.Provider
	})
	return OK(SessionModelsValue{
		Current: modelSelectionValue(current), Routable: routable, Groups: groups, Failures: failures,
	}), nil
}

// SelectModel validates an exact route and applies it to the next assembled step.
func (owner *SessionGateway) SelectModel(requestContext context.Context, call Request[SessionSelectModelRequest]) (Outcome[SessionSelectModelValue], error) {
	subject, refused := owner.ordinaryAgent(requestContext, call.Payload.SessionID)
	if refused != nil {
		return Fail[SessionSelectModelValue](*refused), nil
	}
	proposed := llm.CallConfig{Provider: call.Payload.Provider, Model: call.Payload.Model}
	if call.Payload.ReasoningEffort != nil {
		proposed.ReasoningEffort = llm.ReasoningEffortID(*call.Payload.ReasoningEffort)
	}
	resolved, err := owner.models.ResolveCallConfig(requestContext, proposed)
	if err != nil {
		return Fail[SessionSelectModelValue](newRPCError(
			connection.ErrorModelUnavailable, err.Error(), struct {
				Provider string `json:"provider"`
				Model    string `json:"model"`
			}{Provider: call.Payload.Provider, Model: call.Payload.Model},
		)), nil
	}
	if agentContainsImage(subject) {
		metadata, resolveErr := owner.models.ResolveModelInfo(requestContext, resolved.Provider, resolved.Model)
		if resolveErr != nil {
			return Fail[SessionSelectModelValue](newRPCError(
				connection.ErrorModelUnavailable, resolveErr.Error(), struct {
					Provider string `json:"provider"`
					Model    string `json:"model"`
				}{Provider: call.Payload.Provider, Model: call.Payload.Model},
			)), nil
		}
		if len(metadata.InputModalities) != 0 && !slices.Contains(metadata.InputModalities, llm.ModalityImage) {
			return Fail[SessionSelectModelValue](newRPCError(
				connection.ErrorModelUnavailable,
				fmt.Sprintf("Model %q does not accept image input, but this session already contains images; select an image-capable model.", resolved.Model),
				struct {
					Provider string `json:"provider"`
					Model    string `json:"model"`
				}{Provider: call.Payload.Provider, Model: call.Payload.Model},
			)), nil
		}
	}
	selection, err := owner.selectionFor(subject)
	if err != nil {
		return Outcome[SessionSelectModelValue]{}, err
	}
	selected := agent.ModelSelection{
		Provider: resolved.Provider, Model: resolved.Model, ReasoningEffort: resolved.ReasoningEffort,
	}
	selection.SetCurrent(&selected)
	_ = owner.defaults.SaveSelection(requestContext, selected)
	return OK(SessionSelectModelValue{Selected: modelSelectionValue(selected)}), nil
}

// Prompt validates route and provenance before handing one immutable message to Agent.
func (owner *SessionGateway) Prompt(requestContext context.Context, call Request[SessionPromptRequest]) (Outcome[SessionPromptValue], error) {
	subject, refused := owner.ordinaryAgent(requestContext, call.Payload.SessionID)
	if refused != nil {
		return Fail[SessionPromptValue](*refused), nil
	}
	selection, err := owner.selectionFor(subject)
	if err != nil {
		return Outcome[SessionPromptValue]{}, err
	}
	current, _, err := selection.Current()
	if err != nil {
		return Outcome[SessionPromptValue]{}, err
	}
	if !slices.ContainsFunc(owner.models.ListProviders(), func(provider llm.ProviderInfo) bool {
		return provider.ID == current.Provider
	}) {
		return Fail[SessionPromptValue](newRPCError(
			connection.ErrorModelUnavailable,
			fmt.Sprintf("no adapter serves provider %q; select a model for this session", current.Provider),
			struct {
				Provider string `json:"provider"`
				Model    string `json:"model"`
			}{Provider: current.Provider, Model: current.Model},
		)), nil
	}
	canonicalZone := ""
	if call.Payload.ClientTimeZone != nil {
		canonicalZone, err = canonicalClientTimeZone(*call.Payload.ClientTimeZone)
		if err != nil {
			return Fail[SessionPromptValue](newRPCError(
				connection.ErrorInvalidTimeZone,
				"clientTimeZone must be UTC or a valid IANA Area/Location name",
				struct {
					Value string `json:"value"`
				}{Value: *call.Payload.ClientTimeZone},
			)), nil
		}
	}
	content := make([]llm.ContentBlock, 0, len(call.Payload.Content))
	for _, part := range call.Payload.Content {
		switch typedPart := part.(type) {
		case PromptTextPart:
			content = append(content, llm.NewTextBlock(typedPart.Text))
		case PromptImagePart:
			return Fail[SessionPromptValue](newRPCError(
				connection.ErrorAttachment,
				"Image input is unavailable: this deployment mounts no attachment service.",
				struct {
					Reason string `json:"reason"`
				}{Reason: "ATTACHMENT_UNAVAILABLE"},
			)), nil
		}
	}
	originJSON, err := encodePromptSource(call.RPCID, canonicalZone)
	if err != nil {
		return Outcome[SessionPromptValue]{}, err
	}
	origin, err := llm.NewOpaqueMessageSource("user", originJSON)
	if err != nil {
		return Outcome[SessionPromptValue]{}, err
	}
	messageValue, err := llm.NewUserMessage(llm.UserMessageInput{Content: content, Source: origin})
	if err != nil {
		return Outcome[SessionPromptValue]{}, err
	}
	if call.Payload.Mode == "steer" {
		err = subject.Steer(messageValue)
	} else {
		err = subject.Followup(messageValue)
	}
	if err != nil {
		return Fail[SessionPromptValue](newRPCError(
			connection.ErrorAgentBusy, "prompt rejected", struct {
				Reason string `json:"reason"`
			}{Reason: err.Error()},
		)), nil
	}
	return OK(SessionPromptValue{Accepted: true}), nil
}

// UpdateQueue edits, removes, or strictly steers one still-pending occurrence.
func (owner *SessionGateway) UpdateQueue(requestContext context.Context, call Request[SessionUpdateQueueRequest]) (Outcome[AcceptedValue], error) {
	subject, refused := owner.ordinaryAgent(requestContext, call.Payload.SessionID)
	if refused != nil {
		if refused.Code == connection.ErrorSessionNotFound {
			return Fail[AcceptedValue](queueItemNotFoundError(call.Payload.ItemID)), nil
		}
		return Fail[AcceptedValue](*refused), nil
	}
	pendingID := llm.MessageID(call.Payload.ItemID)
	pending := subject.InboxValue()
	messageValue, target, found := locatePending(pending, pendingID)
	if !found {
		return Fail[AcceptedValue](queueItemNotFoundError(call.Payload.ItemID)), nil
	}
	switch action := call.Payload.Action.(type) {
	case EditQueueAction:
		for _, rawBlock := range action.Content {
			var header struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(rawBlock, &header)
			if header.Type != "text" {
				return Fail[AcceptedValue](newRPCError(
					connection.ErrorAttachment, "queue edits accept text content only", struct {
						Reason string `json:"reason"`
					}{Reason: "QUEUE_EDIT_NON_TEXT"},
				)), nil
			}
		}
		replacement, err := replaceMessageContent(messageValue, action.Content)
		if err != nil {
			return Outcome[AcceptedValue]{}, err
		}
		if _, err := pending.Replace(pendingID, replacement); err != nil {
			return Outcome[AcceptedValue]{}, err
		}
	case RemoveQueueAction:
		if _, err := pending.Remove(pendingID); err != nil {
			return Outcome[AcceptedValue]{}, err
		}
	case SteerQueueAction:
		if target != agent.NextTurn || subject.StatusValue() != agent.StatusRunning {
			return Fail[AcceptedValue](newRPCError(
				connection.ErrorSteerUnavailable,
				"current turn no longer accepts steering",
				struct {
					ItemID MessageID `json:"itemId"`
				}{ItemID: call.Payload.ItemID},
			)), nil
		}
		if _, err := pending.Remove(pendingID); err != nil {
			return Outcome[AcceptedValue]{}, err
		}
		if err := subject.Steer(messageValue); err != nil {
			return Outcome[AcceptedValue]{}, err
		}
	}
	return OK(AcceptedValue{Accepted: true}), nil
}

// Cancel preserves pending work while stopping the active ordinary Agent turn.
func (owner *SessionGateway) Cancel(requestContext context.Context, call Request[SessionCancelRequest]) (Outcome[AcceptedValue], error) {
	subject, refused := owner.ordinaryAgent(requestContext, call.Payload.SessionID)
	if refused != nil {
		return Fail[AcceptedValue](*refused), nil
	}
	subject.Cancel(agent.UserCancel{}, agent.CancelOptions{KeepInbox: true})
	return OK(AcceptedValue{Accepted: true}), nil
}

func (owner *SessionGateway) ensureSession(
	requestContext context.Context,
	identifier session.SessionID,
	workingDirectory string,
	requestedPreset *string,
	createMissing bool,
	knownInspection *sessionpersistence.Inspection,
) (agent.Agent, error) {
	owner.creationMutex.Lock()
	if pending := owner.creations[identifier]; pending != nil {
		owner.creationMutex.Unlock()
		select {
		case <-pending.done:
			if pending.err != nil {
				return nil, pending.err
			}
			return owner.assertSessionAdoption(pending.subject, workingDirectory, requestedPreset)
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
	return owner.assertSessionAdoption(pending.subject, workingDirectory, requestedPreset)
}

func (owner *SessionGateway) createOrAdopt(
	requestContext context.Context,
	identifier session.SessionID,
	workingDirectory string,
	createMissing bool,
	knownInspection *sessionpersistence.Inspection,
) (agent.Agent, error) {
	if subject, found := owner.agents.Get(identifier); found {
		return subject, nil
	}
	if conversation, found := owner.sessions.Get(identifier); found {
		return nil, fmt.Errorf("session %q is attached without a live Agent (cwd %v)", identifier, conversation.Header().CWD)
	}
	loaded := sessionpersistence.Inspection{}
	var inspectErr error
	if knownInspection == nil {
		loaded, inspectErr = owner.persistence.Inspect(requestContext, identifier)
	} else {
		loaded = *knownInspection
	}
	if inspectErr == nil {
		return owner.resumeCold(requestContext, loaded)
	}
	var missing *sessionpersistence.NotFoundError
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
	selection := agent.NewModelSelectionRef(func() (agent.ModelSelection, bool, error) {
		conversation, found := owner.sessions.Get(identifier)
		if found {
			return owner.loggedOrDefaultSelection(conversation)
		}
		selected := owner.defaults.CurrentSelection()
		return selected, selected.Provider != "" && selected.Model != "", nil
	})
	setup := agent.SetupFunc(func(_ context.Context, agentScope *plugin.Scope) (agent.SetupCommit, error) {
		_, err := agent.InstallModelSelection(agentScope, selection)
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
	owner.selectionMutex.Lock()
	owner.selections[identifier] = installedSelection{subject: handle.Subject, ref: selection}
	owner.selectionMutex.Unlock()
	return handle.Subject, nil
}

func (owner *SessionGateway) resumeCold(
	requestContext context.Context,
	loaded sessionpersistence.Inspection,
) (agent.Agent, error) {
	identifier := loaded.Header.ID
	if subject, found := owner.agents.Get(identifier); found {
		return subject, nil
	}
	transient, err := session.New(identifier, session.CreateOptions{
		Seed: loaded.Events, Metadata: sessionMetadataFromHeader(loaded.Header),
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
	selection := agent.NewModelSelectionRef(func() (agent.ModelSelection, bool, error) {
		conversation, found := owner.sessions.Get(identifier)
		if !found {
			return agent.ModelSelection{}, false, nil
		}
		return owner.loggedOrDefaultSelection(conversation)
	})
	setup := agent.SetupFunc(func(_ context.Context, agentScope *plugin.Scope) (agent.SetupCommit, error) {
		_, err := agent.InstallModelSelection(agentScope, selection)
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
	owner.selectionMutex.Lock()
	owner.selections[identifier] = installedSelection{subject: handle.Subject, ref: selection}
	owner.selectionMutex.Unlock()
	return handle.Subject, nil
}

func (owner *SessionGateway) assertSessionAdoption(
	subject agent.Agent,
	workingDirectory string,
	requestedPreset *string,
) (agent.Agent, error) {
	header := subject.SessionValue().Header()
	if header.Origin == session.OriginSubagent || owner.hasLiveParent(subject, header) {
		return nil, &subagentSessionOwnership{identifier: subject.ID()}
	}
	if requestedPreset != nil && (header.AgentPreset == nil || *header.AgentPreset != *requestedPreset) {
		return nil, &sessionPresetConflict{
			identifier: subject.ID(), requested: *requestedPreset,
			existing: cloneStringPointer(header.AgentPreset),
		}
	}
	return owner.assertCWD(subject, workingDirectory)
}

func (owner *SessionGateway) assertCWD(subject agent.Agent, requested string) (agent.Agent, error) {
	header := subject.SessionValue().Header()
	if header.CWD == nil || *header.CWD != requested {
		return nil, &sessionCWDConflict{identifier: subject.ID(), requested: requested, existing: cloneStringPointer(header.CWD)}
	}
	return subject, nil
}

func (owner *SessionGateway) ordinaryAgent(
	requestContext context.Context,
	identifier SessionID,
) (agent.Agent, *connection.RPCError) {
	subject, found := owner.agents.Get(session.SessionID(identifier))
	if !found {
		loaded, err := owner.persistence.Inspect(requestContext, session.SessionID(identifier))
		if err != nil {
			var missing *sessionpersistence.NotFoundError
			if errors.As(err, &missing) {
				problem := sessionNotFoundError(identifier)
				return nil, &problem
			}
			problem := newRPCError(connection.ErrorInternal, err.Error(), struct{}{})
			return nil, &problem
		}
		workingDirectory := ""
		if loaded.Header.CWD != nil {
			workingDirectory = *loaded.Header.CWD
		}
		subject, err = owner.ensureSession(
			requestContext, loaded.Header.ID, workingDirectory, loaded.Header.AgentPreset, false, &loaded,
		)
		if err != nil {
			var disappeared *sessionpersistence.NotFoundError
			if errors.As(err, &disappeared) {
				problem := sessionNotFoundError(identifier)
				return nil, &problem
			}
			problem := newRPCError(connection.ErrorInternal, err.Error(), struct{}{})
			return nil, &problem
		}
	}
	header := subject.SessionValue().Header()
	if header.Origin == session.OriginSubagent || owner.hasLiveParent(subject, header) {
		problem := subagentOwnershipError(identifier)
		return nil, &problem
	}
	return subject, nil
}

func summarizeSession(
	metadata session.Header,
	entries []session.Event,
	running bool,
	projectionSnapshot sessionprojection.Snapshot,
) SessionSummary {
	blank := true
	updatedAt := metadata.CreatedAt
	for _, committed := range entries {
		if committed.Type == session.TurnStartEventName {
			blank = false
		}
		if committed.Type == session.UserMessageEventName && directUserEvent(committed) && committed.Time > updatedAt {
			updatedAt = committed.Time
		}
	}
	summary := SessionSummary{
		SessionID: SessionID(metadata.ID), UpdatedAt: updatedAt, Running: running, Blank: blank,
		Origin: string(metadata.Origin), CWD: cloneStringPointer(metadata.CWD),
		AgentPreset: cloneStringPointer(metadata.AgentPreset),
	}
	if metadata.ParentSession != nil {
		summary.ParentSessionID = SessionID(*metadata.ParentSession)
	}
	if len(projectionSnapshot.Values) != 0 {
		summary.Projections = projectionBlock(projectionSnapshot)
	}
	return summary
}

func sessionMetadataFromHeader(metadata session.Header) session.Metadata {
	createdAt := metadata.CreatedAt
	return session.Metadata{
		CreatedAt: &createdAt, CWD: cloneStringPointer(metadata.CWD),
		ParentSession: cloneSessionIDPointer(metadata.ParentSession), SeedLength: cloneInt64Pointer(metadata.SeedLength),
		Origin: metadata.Origin, DelegationDepth: cloneInt64Pointer(metadata.DelegationDepth),
		AgentPreset: cloneStringPointer(metadata.AgentPreset),
	}
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

func (owner *SessionGateway) hasLiveParent(subject agent.Agent, header session.Header) bool {
	if header.ParentSession == nil {
		return false
	}
	parent, found := owner.agents.Get(*header.ParentSession)
	return found && owner.agents.IsOwnedBy(subject.ID(), parent)
}

func (owner *SessionGateway) selectionFor(subject agent.Agent) (*agent.ModelSelectionRef, error) {
	identifier := subject.ID()
	owner.selectionMutex.Lock()
	defer owner.selectionMutex.Unlock()
	if installed, found := owner.selections[identifier]; found && installed.subject == subject {
		return installed.ref, nil
	}
	selection := agent.NewModelSelectionRef(func() (agent.ModelSelection, bool, error) {
		return owner.loggedOrDefaultSelection(subject.SessionValue())
	})
	if _, err := agent.InstallModelSelection(subject.ScopeValue(), selection); err != nil {
		return nil, err
	}
	owner.selections[identifier] = installedSelection{subject: subject, ref: selection}
	return selection, nil
}

func (owner *SessionGateway) loggedOrDefaultSelection(
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

func (owner *SessionGateway) modelCatalog(requestContext context.Context) ([]ModelProviderGroup, []ModelCatalogFailure) {
	providers := owner.models.ListProviders()
	groups := make([]ModelProviderGroup, 0, len(providers))
	failures := make([]ModelCatalogFailure, 0)
	for _, provider := range providers {
		listedModels, err := owner.models.ListModels(requestContext, provider.ID)
		if err != nil {
			failures = append(failures, ModelCatalogFailure{ID: provider.ID, Name: provider.Name, Message: err.Error()})
			continue
		}
		entries := make([]ModelCatalogModel, 0, len(listedModels))
		providerFailed := false
		for _, listed := range listedModels {
			resolved, resolveErr := owner.models.ResolveModelInfo(requestContext, provider.ID, listed.ID)
			if resolveErr != nil {
				failures = append(failures, ModelCatalogFailure{ID: provider.ID, Name: provider.Name, Message: resolveErr.Error()})
				providerFailed = true
				break
			}
			entry := ModelCatalogModel{ID: listed.ID, Name: listed.Name, Description: listed.Description}
			if resolved.Reasoning != nil {
				reasoning := &ModelReasoning{
					Efforts:       make([]ModelReasoningEffort, 0, len(resolved.Reasoning.Efforts)),
					DefaultEffort: string(resolved.Reasoning.DefaultEffort),
				}
				for _, effort := range resolved.Reasoning.Efforts {
					reasoning.Efforts = append(reasoning.Efforts, ModelReasoningEffort{
						ID: string(effort.ID), Name: effort.Name, Description: effort.Description,
					})
				}
				entry.Reasoning = reasoning
			}
			entries = append(entries, entry)
		}
		if !providerFailed && len(entries) != 0 {
			groups = append(groups, ModelProviderGroup{ID: provider.ID, Name: provider.Name, Models: entries})
		}
	}
	if groups == nil {
		groups = []ModelProviderGroup{}
	}
	if failures == nil {
		failures = []ModelCatalogFailure{}
	}
	return groups, failures
}

func (owner *SessionGateway) observeSessionEvent(_ context.Context, conversation *session.Session, committed session.Event) error {
	projected, err := projectSessionEvent(committed)
	if err != nil {
		return err
	}
	var queue []QueuedInboxItem
	queueChanged := committed.Type == "agent/inbox/spliced"
	if queueChanged {
		queue, err = projectQueue(conversation.Header(), conversation.Events())
		if err != nil {
			return err
		}
	}
	return owner.hub.sessionEvent(conversation.ID(), projected, queueChanged, queue)
}

func (owner *SessionGateway) observeProjectionChange(projectionChange sessionprojection.Change) {
	_ = owner.hub.sessionProjection(
		projectionChange.Session.ID(), projectionChange.Key, projectionChange.Value, projectionChange.Seq,
	)
}

func projectionBlock(source sessionprojection.Snapshot) *SessionProjectionsBlock {
	values := make(map[string]json.RawMessage, len(source.Values))
	for key, rawValue := range source.Values {
		values[key] = append(json.RawMessage(nil), rawValue...)
	}
	return &SessionProjectionsBlock{AsOfSeq: source.AsOfSeq, Values: values}
}

func (owner *SessionGateway) observeSessionCreated(_ context.Context, conversation *session.Session) error {
	return owner.hub.sessionCreated(conversation)
}

func (owner *SessionGateway) observeSessionDisposed(_ context.Context, conversation *session.Session) error {
	return owner.hub.sessionDisposed(conversation.ID())
}

func (owner *SessionGateway) observeAgentStatus(_ context.Context, subject agent.Agent, status agent.Status) error {
	return owner.hub.agentStatus(subject.ID(), status == agent.StatusRunning)
}

func (owner *SessionGateway) observeAgentError(_ context.Context, notice agent.ErrorNotice) error {
	return owner.hub.agentError(notice.Subject.ID(), notice.Err)
}

func (owner *SessionGateway) observeAgentDisposed(_ context.Context, subject agent.Agent) error {
	owner.selectionMutex.Lock()
	if installed, found := owner.selections[subject.ID()]; found && installed.subject == subject {
		delete(owner.selections, subject.ID())
	}
	owner.selectionMutex.Unlock()
	return nil
}

type sessionCWDConflict struct {
	identifier session.SessionID
	requested  string
	existing   *string
}

type sessionPresetConflict struct {
	identifier session.SessionID
	requested  string
	existing   *string
}

func (problem *sessionPresetConflict) Error() string {
	if problem.existing == nil {
		return fmt.Sprintf(
			"session %q records no agent preset, so it cannot be adopted under %q",
			problem.identifier, problem.requested,
		)
	}
	return fmt.Sprintf(
		"session %q already runs agent preset %q; requested %q",
		problem.identifier, *problem.existing, problem.requested,
	)
}

type subagentSessionOwnership struct {
	identifier session.SessionID
}

func (problem *subagentSessionOwnership) Error() string {
	return fmt.Sprintf("session %q is owned by subagent routing", problem.identifier)
}

func (problem *sessionCWDConflict) Error() string {
	return fmt.Sprintf(
		"session %q already exists with cwd %s; requested %q",
		problem.identifier, quotedOptional(problem.existing), problem.requested,
	)
}

func quotedOptional(textValue *string) string {
	if textValue == nil {
		return "undefined"
	}
	return fmt.Sprintf("%q", *textValue)
}

func historyPage(events []session.Event, beforeSeq *int64, maxMessages int64) ([]HistoryEntry, bool, error) {
	window := events
	if beforeSeq != nil {
		window = make([]session.Event, 0, len(events))
		for _, committed := range events {
			if committed.Seq < *beforeSeq {
				window = append(window, committed)
			}
		}
	}
	cut := int64(0)
	count := int64(0)
	for index := len(window) - 1; index >= 0; index-- {
		committed := window[index]
		if (committed.Type != session.UserMessageEventName && committed.Type != session.AssistantMessageEventName) ||
			committed.SurfaceOp == nil || committed.SurfaceOp.Kind != session.SurfaceOperationAppend {
			continue
		}
		count++
		groupStart := committed.Seq
		if committed.SourceEventSeqs != nil && len(*committed.SourceEventSeqs) != 0 {
			for _, sourceSeq := range *committed.SourceEventSeqs {
				if sourceSeq < groupStart {
					groupStart = sourceSeq
				}
			}
		}
		if count >= maxMessages {
			cut = groupStart
			break
		}
	}
	page := make([]HistoryEntry, 0, len(window))
	for _, committed := range window {
		if committed.Seq < cut {
			continue
		}
		projected, err := projectSessionEvent(committed)
		if err != nil {
			return nil, false, err
		}
		page = append(page, HistoryEntry{Event: projected})
	}
	if page == nil {
		page = []HistoryEntry{}
	}
	return page, cut > 0, nil
}

func projectSessionEvent(committed session.Event) (SessionEvent, error) {
	projected := SessionEvent{
		Type: committed.Type, Seq: committed.Seq, Time: committed.Time,
		Data: append(json.RawMessage(nil), committed.Data...), Ignorable: committed.Ignorable,
	}
	if committed.SourceEventSeqs != nil {
		provenance := append([]int64(nil), (*committed.SourceEventSeqs)...)
		projected.SourceEventSeqs = &provenance
	}
	if committed.SurfaceOp != nil {
		encoded, err := json.Marshal(committed.SurfaceOp)
		if err != nil {
			return SessionEvent{}, err
		}
		projected.SurfaceOp = encoded
	}
	return projected, nil
}

func directUserEvent(committed session.Event) bool {
	var envelope struct {
		Source struct {
			Kind string `json:"kind"`
		} `json:"source"`
	}
	return json.Unmarshal(committed.Data, &envelope) == nil && envelope.Source.Kind == "user"
}

func modelSelectionValue(selected agent.ModelSelection) ModelSelection {
	return ModelSelection{
		Provider: selected.Provider, Model: selected.Model,
		ReasoningEffort: string(selected.ReasoningEffort),
	}
}

func agentContainsImage(subject agent.Agent) bool {
	for _, messageValue := range append(subject.InboxValue().NextTurn(), subject.InboxValue().NextStep()...) {
		if llm.ContentHasImage(messageValue.ContentValue()) {
			return true
		}
	}
	messages, err := subject.SessionValue().DeriveMessages()
	if err != nil {
		return false
	}
	for _, messageValue := range messages {
		if llm.ContentHasImage(messageValue.ContentValue()) {
			return true
		}
	}
	return false
}

func canonicalClientTimeZone(input string) (string, error) {
	if input == "UTC" {
		return input, nil
	}
	if input == "" || input != filepath.Clean(input) || !ianaTimeZonePattern.MatchString(input) {
		return "", errors.New("invalid IANA time zone")
	}
	location, err := time.LoadLocation(input)
	if err != nil {
		return "", err
	}
	return location.String(), nil
}

func encodePromptSource(rpcID connection.RPCID, canonicalZone string) (json.RawMessage, error) {
	fields := struct {
		Kind           string           `json:"kind"`
		RPCID          connection.RPCID `json:"rpcId"`
		ClientTimeZone string           `json:"clientTimeZone,omitempty"`
	}{Kind: "user", RPCID: rpcID, ClientTimeZone: canonicalZone}
	return json.Marshal(fields)
}

func locatePending(pending *agent.Inbox, identifier llm.MessageID) (llm.UserMessage, agent.InboxTarget, bool) {
	for _, candidate := range pending.NextTurn() {
		if candidate.StableID() == identifier {
			return candidate, agent.NextTurn, true
		}
	}
	for _, candidate := range pending.NextStep() {
		if candidate.StableID() == identifier {
			return candidate, agent.NextStep, true
		}
	}
	return llm.UserMessage{}, "", false
}

func replaceMessageContent(messageValue llm.UserMessage, content []json.RawMessage) (llm.UserMessage, error) {
	origin, err := json.Marshal(messageValue.SourceValue())
	if err != nil {
		return llm.UserMessage{}, err
	}
	if content == nil {
		content = []json.RawMessage{}
	}
	wireValue := struct {
		ID      llm.MessageID     `json:"id"`
		Role    llm.MessageRole   `json:"role"`
		Content []json.RawMessage `json:"content"`
		Source  json.RawMessage   `json:"source"`
	}{
		ID: messageValue.StableID(), Role: llm.RoleUser,
		Content: content, Source: origin,
	}
	encoded, err := json.Marshal(wireValue)
	if err != nil {
		return llm.UserMessage{}, err
	}
	return llm.DecodeUserMessage(encoded)
}

func sessionNotFoundError(identifier SessionID) connection.RPCError {
	return newRPCError(
		connection.ErrorSessionNotFound,
		fmt.Sprintf("session %q not found (not attached)", identifier),
		struct {
			SessionID SessionID `json:"sessionId"`
		}{SessionID: identifier},
	)
}

func subagentOwnershipError(identifier SessionID) connection.RPCError {
	return newRPCError(
		connection.ErrorAgentBusy,
		fmt.Sprintf("session %q is owned by subagent routing", identifier),
		struct {
			Reason string `json:"reason"`
		}{Reason: "use subagent delivery for this child session"},
	)
}

func queueItemNotFoundError(identifier MessageID) connection.RPCError {
	return newRPCError(
		connection.ErrorQueueItemNotFound, "queued item is no longer pending",
		struct {
			ItemID MessageID `json:"itemId"`
		}{ItemID: identifier},
	)
}

func cloneStringPointer(source *string) *string {
	if source == nil {
		return nil
	}
	copyValue := *source
	return &copyValue
}

func mintSessionID() (session.SessionID, error) {
	randomID, err := mintUUID()
	if err != nil {
		return "", err
	}
	return session.SessionID("session-" + randomID), nil
}

func mintFrameRPCID() (connection.RPCID, error) {
	randomID, err := mintUUID()
	return connection.RPCID(randomID), err
}

func mintUUID() (string, error) {
	var randomBytes [16]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		return "", err
	}
	randomBytes[6] = randomBytes[6]&0x0f | 0x40
	randomBytes[8] = randomBytes[8]&0x3f | 0x80
	return fmt.Sprintf(
		"%x-%x-%x-%x-%x",
		randomBytes[0:4], randomBytes[4:6], randomBytes[6:8], randomBytes[8:10], randomBytes[10:16],
	), nil
}
