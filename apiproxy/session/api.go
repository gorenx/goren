package sessionapi

import (
	"context"
	"encoding/json"
	imageapi "github.com/gorenx/goren/apiproxy/session/image"

	workspaceapi "github.com/gorenx/goren/apiproxy/workspace"

	llmapi "github.com/gorenx/goren/apiproxy/llm"

	rpc "github.com/gorenx/goren/apiproxy/rpc"

	sessionframes "github.com/gorenx/goren/apiproxy/session/events"
)

const (
	ListMethod        = "session.list"
	SearchMethod      = "session.search"
	CreateMethod      = "session.create"
	RenameMethod      = "session.rename"
	HistoryMethod     = "session.history"
	ModelsMethod      = "session.models"
	SelectModelMethod = "session.selectModel"
	PromptMethod      = "session.prompt"
	UpdateQueueMethod = "session.updateQueue"
	CancelMethod      = "session.cancel"
)

// ListRequest selects one newest-first page of visible Sessions.
type ListRequest struct {
	Cursor *string
	Limit  *int64
}

// Summary is the browser-visible projection of one attached Session.
type Summary struct {
	SessionID       sessionframes.SessionID    `json:"sessionId"`
	UpdatedAt       int64                      `json:"updatedAt"`
	Running         bool                       `json:"running"`
	Blank           bool                       `json:"blank"`
	ParentSessionID sessionframes.SessionID    `json:"parentSessionId,omitempty"`
	Origin          string                     `json:"origin,omitempty"`
	CWD             *string                    `json:"cwd,omitempty"`
	AgentPreset     *string                    `json:"agentPreset,omitempty"`
	Projections     *sessionframes.Projections `json:"projections,omitempty"`
}

// ListValue is one page of session.list results.
type ListValue struct {
	Items      []Summary `json:"items"`
	NextCursor *string   `json:"nextCursor,omitempty"`
}

// SearchRequest contains the normalized literal sidebar query.
type SearchRequest struct {
	Query string
}

// SearchItem contains content-search identity and a bounded plain-text
// excerpt; display metadata remains owned by session.list.
type SearchItem struct {
	SessionID sessionframes.SessionID `json:"sessionId"`
	Snippet   string                  `json:"snippet"`
}

// SearchValue is one bounded, cursorless sidebar result.
type SearchValue struct {
	Items   []SearchItem `json:"items"`
	HasMore bool         `json:"hasMore"`
}

// CreateRequest creates or idempotently adopts an ordinary Session.
type CreateRequest struct {
	WorkspaceID *workspaceapi.ID
	CWD         *string
	SessionID   *sessionframes.SessionID
	AgentPreset *string
}

// CreateValue identifies the published Session composition.
type CreateValue struct {
	SessionID   sessionframes.SessionID `json:"sessionId"`
	AgentPreset *string                 `json:"agentPreset,omitempty"`
}

// RenameRequest contains raw user title input; the title owner normalizes it.
type RenameRequest struct {
	SessionID sessionframes.SessionID
	Title     string
}

// RenameValue returns the accepted normalized value and title event seq.
type RenameValue struct {
	Title string `json:"title"`
	Seq   int64  `json:"seq"`
}

// HistoryRequest pages backwards on whole message boundaries.
type HistoryRequest struct {
	SessionID   sessionframes.SessionID
	BeforeSeq   *int64
	MaxMessages *int64
}

// HistoryEntry pairs one raw event with an optional host-computed Tool view.
type HistoryEntry struct {
	Event sessionframes.LogEvent  `json:"event"`
	View  *sessionframes.ToolView `json:"view,omitempty"`
}

// HistoryValue is one contiguous raw history window.
type HistoryValue struct {
	Events      []HistoryEntry             `json:"events"`
	HasMore     bool                       `json:"hasMore"`
	Projections *sessionframes.Projections `json:"projections,omitempty"`
}

// ModelsRequest addresses one ordinary live Session.
type ModelsRequest struct {
	SessionID sessionframes.SessionID
}

// ModelSelection is the API wire projection of one complete Agent selection.
type ModelSelection struct {
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
}

// ModelsValue is a fresh session-scoped model directory.
type ModelsValue struct {
	// Current is nil when the Session has no running Agent; querying never starts one.
	Current  *ModelSelection              `json:"current"`
	Routable bool                         `json:"routable"`
	Groups   []llmapi.ModelProviderGroup  `json:"groups"`
	Failures []llmapi.ModelCatalogFailure `json:"failures"`
}

// SelectModelRequest changes the next-step model selection.
type SelectModelRequest struct {
	SessionID       sessionframes.SessionID
	Provider        string
	Model           string
	ReasoningEffort *string
}

// SelectModelValue echoes the exact resolved route.
type SelectModelValue struct {
	Selected ModelSelection `json:"selected"`
}

// PromptContentPart is the closed browser prompt-input union.
type PromptContentPart interface {
	PromptContentPart()
}

// PromptTextPart is one browser-submitted text block.
type PromptTextPart struct {
	Text string
}

func (PromptTextPart) PromptContentPart() {}

// PromptRequest sends one ordinary Agent message.
type PromptRequest struct {
	SessionID      sessionframes.SessionID
	Mode           string
	Content        []PromptContentPart
	ClientTimeZone *string
}

// PromptValue acknowledges durable inbox admission.
type PromptValue struct {
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

// UpdateQueueRequest mutates one pending message occurrence.
type UpdateQueueRequest struct {
	SessionID sessionframes.SessionID
	ItemID    sessionframes.MessageID
	Action    QueueAction
}

// CancelRequest stops one ordinary live Agent turn.
type CancelRequest struct {
	SessionID sessionframes.SessionID
}

// AcceptedValue is shared by prompt, updateQueue, and cancel successes.
type AcceptedValue struct {
	Accepted bool `json:"accepted"`
}

// API owns lifecycle, history, model selection, and interaction methods.
// Content search is a separate capability because it has its own Query and
// visibility dependencies.
type API interface {
	List(context.Context, rpc.Request[ListRequest]) (rpc.Outcome[ListValue], error)
	Create(context.Context, rpc.Request[CreateRequest]) (rpc.Outcome[CreateValue], error)
	Rename(context.Context, rpc.Request[RenameRequest]) (rpc.Outcome[RenameValue], error)
	History(context.Context, rpc.Request[HistoryRequest]) (rpc.Outcome[HistoryValue], error)
	Models(context.Context, rpc.Request[ModelsRequest]) (rpc.Outcome[ModelsValue], error)
	SelectModel(context.Context, rpc.Request[SelectModelRequest]) (rpc.Outcome[SelectModelValue], error)
	Attachment(context.Context, rpc.Request[imageapi.AttachmentRequest]) (rpc.Outcome[imageapi.AttachmentValue], error)
	Prompt(context.Context, rpc.Request[PromptRequest]) (rpc.Outcome[PromptValue], error)
	UpdateQueue(context.Context, rpc.Request[UpdateQueueRequest]) (rpc.Outcome[AcceptedValue], error)
	Cancel(context.Context, rpc.Request[CancelRequest]) (rpc.Outcome[AcceptedValue], error)
}

// SearchAPI owns only session.search.
type SearchAPI interface {
	Search(context.Context, rpc.Request[SearchRequest]) (rpc.Outcome[SearchValue], error)
}

// Register installs every currently included session method.
func Register(methods *rpc.Catalog, sessionMethods API, searchMethods SearchAPI) error {
	registrations := []func() error{
		func() error {
			return rpc.RegisterUnary(methods, imageapi.AttachmentMethod, imageapi.DecodeAttachmentRequest, sessionMethods.Attachment)
		},
		func() error {
			return rpc.RegisterUnary(methods, ListMethod, DecodeListRequest, sessionMethods.List)
		},
		func() error {
			return rpc.RegisterUnary(methods, SearchMethod, DecodeSearchRequest, searchMethods.Search)
		},
		func() error {
			return rpc.RegisterUnary(methods, CreateMethod, DecodeCreateRequest, sessionMethods.Create)
		},
		func() error {
			return rpc.RegisterUnary(methods, RenameMethod, DecodeRenameRequest, sessionMethods.Rename)
		},
		func() error {
			return rpc.RegisterUnary(methods, HistoryMethod, DecodeHistoryRequest, sessionMethods.History)
		},
		func() error {
			return rpc.RegisterUnary(methods, ModelsMethod, DecodeModelsRequest, sessionMethods.Models)
		},
		func() error {
			return rpc.RegisterUnary(methods, SelectModelMethod, DecodeSelectModelRequest, sessionMethods.SelectModel)
		},
		func() error {
			return rpc.RegisterUnary(methods, PromptMethod, DecodePromptRequest, sessionMethods.Prompt)
		},
		func() error {
			return rpc.RegisterUnary(methods, UpdateQueueMethod, DecodeUpdateQueueRequest, sessionMethods.UpdateQueue)
		},
		func() error {
			return rpc.RegisterUnary(methods, CancelMethod, DecodeCancelRequest, sessionMethods.Cancel)
		},
	}
	for _, register := range registrations {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}
