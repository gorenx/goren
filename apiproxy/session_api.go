package apiproxy

import (
	"context"
)

const (
	SessionListMethod        = "session.list"
	SessionSearchMethod      = "session.search"
	SessionCreateMethod      = "session.create"
	SessionRenameMethod      = "session.rename"
	SessionHistoryMethod     = "session.history"
	SessionModelsMethod      = "session.models"
	SessionSelectModelMethod = "session.selectModel"
	SessionPromptMethod      = "session.prompt"
	SessionUpdateQueueMethod = "session.updateQueue"
	SessionCancelMethod      = "session.cancel"
)

// SessionListRequest keeps the source cursor seat although v1 does not page.
type SessionListRequest struct {
	Cursor *string
}

// SessionSummary is the browser-visible projection of one attached Session.
type SessionSummary struct {
	SessionID       SessionID                `json:"sessionId"`
	UpdatedAt       int64                    `json:"updatedAt"`
	Running         bool                     `json:"running"`
	Blank           bool                     `json:"blank"`
	ParentSessionID SessionID                `json:"parentSessionId,omitempty"`
	Origin          string                   `json:"origin,omitempty"`
	CWD             *string                  `json:"cwd,omitempty"`
	AgentPreset     *string                  `json:"agentPreset,omitempty"`
	Projections     *SessionProjectionsBlock `json:"projections,omitempty"`
}

// SessionProjectionsBlock is one consistent whole-value projection cut.
type SessionProjectionsBlock struct {
	AsOfSeq int64                      `json:"asOfSeq"`
	Values  map[string]json.RawMessage `json:"values"`
}

// SessionListValue is the complete session.list result.
type SessionListValue struct {
	Items []SessionSummary `json:"items"`
}

// SessionSearchRequest contains the normalized literal sidebar query.
type SessionSearchRequest struct {
	Query string
}

// SessionSearchItem contains content-search identity and a bounded plain-text
// excerpt; display metadata remains owned by session.list.
type SessionSearchItem struct {
	SessionID SessionID `json:"sessionId"`
	Snippet   string    `json:"snippet"`
}

// SessionSearchValue is one bounded, cursorless sidebar result.
type SessionSearchValue struct {
	Items   []SessionSearchItem `json:"items"`
	HasMore bool                `json:"hasMore"`
}

// SessionCreateRequest creates or idempotently adopts an ordinary Session.
type SessionCreateRequest struct {
	WorkspaceID *WorkspaceID
	CWD         *string
	SessionID   *SessionID
	AgentPreset *string
}

// SessionCreateValue identifies the published Session composition.
type SessionCreateValue struct {
	SessionID   SessionID `json:"sessionId"`
	AgentPreset *string   `json:"agentPreset,omitempty"`
}

// SessionRenameRequest contains raw user title input; the title owner normalizes it.
type SessionRenameRequest struct {
	SessionID SessionID
	Title     string
}

// SessionRenameValue returns the accepted normalized value and title event seq.
type SessionRenameValue struct {
	Title string `json:"title"`
	Seq   int64  `json:"seq"`
}

// SessionHistoryRequest pages backwards on whole message boundaries.
type SessionHistoryRequest struct {
	SessionID   SessionID
	BeforeSeq   *int64
	MaxMessages *int64
}

// HistoryEntry pairs one raw event with an optional host-computed Tool view.
type HistoryEntry struct {
	Event SessionEvent   `json:"event"`
	View  *ToolEventView `json:"view,omitempty"`
}

// SessionHistoryValue is one contiguous raw history window.
type SessionHistoryValue struct {
	Events      []HistoryEntry           `json:"events"`
	HasMore     bool                     `json:"hasMore"`
	Projections *SessionProjectionsBlock `json:"projections,omitempty"`
}

// SessionModelsRequest addresses one ordinary live Session.
type SessionModelsRequest struct {
	SessionID SessionID
}

// ModelSelection is the API wire projection of one complete Agent selection.
type ModelSelection struct {
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
}

// ModelReasoningEffort is one adapter-owned selector value.
type ModelReasoningEffort struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ModelReasoning describes exact-model reasoning choices.
type ModelReasoning struct {
	Efforts       []ModelReasoningEffort `json:"efforts"`
	DefaultEffort string                 `json:"defaultEffort,omitempty"`
}

// ModelCatalogModel is one advisory provider model row.
type ModelCatalogModel struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Reasoning   *ModelReasoning `json:"reasoning,omitempty"`
}

// ModelProviderGroup is one successfully loaded provider catalog.
type ModelProviderGroup struct {
	ID     string              `json:"id"`
	Name   string              `json:"name"`
	Models []ModelCatalogModel `json:"models"`
}

// ModelCatalogFailure contains one provider-local catalog error.
type ModelCatalogFailure struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Message string `json:"message"`
}

// SessionModelsValue is a fresh session-scoped model directory.
type SessionModelsValue struct {
	Current  ModelSelection        `json:"current"`
	Routable bool                  `json:"routable"`
	Groups   []ModelProviderGroup  `json:"groups"`
	Failures []ModelCatalogFailure `json:"failures"`
}

// SessionSelectModelRequest changes the next-step model selection.
type SessionSelectModelRequest struct {
	SessionID       SessionID
	Provider        string
	Model           string
	ReasoningEffort *string
}

// SessionSelectModelValue echoes the exact resolved route.
type SessionSelectModelValue struct {
	Selected ModelSelection `json:"selected"`
}

// PromptContentPart is the closed browser prompt-input union.
type PromptContentPart interface {
	promptContentPart()
}

// PromptTextPart is one browser-submitted text block.
type PromptTextPart struct {
	Text string
}

func (PromptTextPart) promptContentPart() {}

// PromptImagePart is one temporary browser image awaiting durable admission.
type PromptImagePart struct {
	MediaType string
	Data      string
	Name      *string
}

func (PromptImagePart) promptContentPart() {}

// SessionPromptRequest sends one ordinary Agent message.
type SessionPromptRequest struct {
	SessionID      SessionID
	Mode           string
	Content        []PromptContentPart
	ClientTimeZone *string
}

// SessionPromptValue acknowledges durable inbox admission.
type SessionPromptValue struct {
	Accepted bool `json:"accepted"`
}

// QueueAction is the closed mutation union for one pending occurrence.
type QueueAction interface {
	queueAction()
}

// EditQueueAction replaces one pending message's content.
type EditQueueAction struct {
	Content []json.RawMessage
}

func (EditQueueAction) queueAction() {}

// RemoveQueueAction drops one pending occurrence.
type RemoveQueueAction struct{}

func (RemoveQueueAction) queueAction() {}

// SteerQueueAction moves one next-turn occurrence into the active step.
type SteerQueueAction struct{}

func (SteerQueueAction) queueAction() {}

// SessionUpdateQueueRequest mutates one pending message occurrence.
type SessionUpdateQueueRequest struct {
	SessionID SessionID
	ItemID    MessageID
	Action    QueueAction
}

// SessionCancelRequest stops one ordinary live Agent turn.
type SessionCancelRequest struct {
	SessionID SessionID
}

// AcceptedValue is shared by prompt, updateQueue, and cancel successes.
type AcceptedValue struct {
	Accepted bool `json:"accepted"`
}

// SessionAPI owns lifecycle, history, model selection, and interaction methods.
// Content search is a separate capability because it has its own Query and
// visibility dependencies.
type SessionAPI interface {
	List(context.Context, Request[SessionListRequest]) (Outcome[SessionListValue], error)
	Create(context.Context, Request[SessionCreateRequest]) (Outcome[SessionCreateValue], error)
	Rename(context.Context, Request[SessionRenameRequest]) (Outcome[SessionRenameValue], error)
	History(context.Context, Request[SessionHistoryRequest]) (Outcome[SessionHistoryValue], error)
	Models(context.Context, Request[SessionModelsRequest]) (Outcome[SessionModelsValue], error)
	SelectModel(context.Context, Request[SessionSelectModelRequest]) (Outcome[SessionSelectModelValue], error)
	Prompt(context.Context, Request[SessionPromptRequest]) (Outcome[SessionPromptValue], error)
	UpdateQueue(context.Context, Request[SessionUpdateQueueRequest]) (Outcome[AcceptedValue], error)
	Cancel(context.Context, Request[SessionCancelRequest]) (Outcome[AcceptedValue], error)
}

// SessionSearchAPI owns only session.search.
type SessionSearchAPI interface {
	Search(context.Context, Request[SessionSearchRequest]) (Outcome[SessionSearchValue], error)
}

// RegisterSessionAPI installs every currently included session method.
func RegisterSessionAPI(methods *Catalog, service SessionAPI, searchService SessionSearchAPI) error {
	registrations := []func() error{
		func() error { return RegisterUnary(methods, SessionListMethod, DecodeSessionListRequest, service.List) },
		func() error {
			return RegisterUnary(methods, SessionSearchMethod, DecodeSessionSearchRequest, searchService.Search)
		},
		func() error {
			return RegisterUnary(methods, SessionCreateMethod, DecodeSessionCreateRequest, service.Create)
		},
		func() error {
			return RegisterUnary(methods, SessionRenameMethod, DecodeSessionRenameRequest, service.Rename)
		},
		func() error {
			return RegisterUnary(methods, SessionHistoryMethod, DecodeSessionHistoryRequest, service.History)
		},
		func() error {
			return RegisterUnary(methods, SessionModelsMethod, DecodeSessionModelsRequest, service.Models)
		},
		func() error {
			return RegisterUnary(methods, SessionSelectModelMethod, DecodeSessionSelectModelRequest, service.SelectModel)
		},
		func() error {
			return RegisterUnary(methods, SessionPromptMethod, DecodeSessionPromptRequest, service.Prompt)
		},
		func() error {
			return RegisterUnary(methods, SessionUpdateQueueMethod, DecodeSessionUpdateQueueRequest, service.UpdateQueue)
		},
		func() error {
			return RegisterUnary(methods, SessionCancelMethod, DecodeSessionCancelRequest, service.Cancel)
		},
	}
	for _, register := range registrations {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}
