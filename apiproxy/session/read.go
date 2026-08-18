package sessionapi

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/gorenx/goren/agent"
	api "github.com/gorenx/goren/apiproxy"
	"github.com/gorenx/goren/session"
	sesspersist "github.com/gorenx/goren/session/persistence"
	sessionprojection "github.com/gorenx/goren/session/projection"
)

const defaultHistoryMessages int64 = 50

type sessionReader struct {
	agents      agent.Registry
	sessions    session.LiveStore
	persistence sesspersist.Persistence
	projections sessionprojection.Registry
}

func (view *sessionReader) List(requestContext context.Context, _ api.Request[api.SessionListRequest]) (api.Outcome[api.SessionListValue], error) {
	items, err := view.visibleSessionSummaries(requestContext)
	if err != nil {
		return api.Outcome[api.SessionListValue]{}, err
	}
	return api.OK(api.SessionListValue{Items: items}), nil
}

func (view *sessionReader) visibleSessionSummaries(requestContext context.Context) ([]api.SessionSummary, error) {
	items := make([]api.SessionSummary, 0)
	liveIDs := make(map[session.SessionID]struct{})
	for _, conversation := range view.sessions.List() {
		header := conversation.Header()
		if header.CWD == nil {
			continue
		}
		events := conversation.Events()
		liveIDs[header.ID] = struct{}{}
		projectionSnapshot, projectionErr := view.projections.Snapshot(conversation)
		if projectionErr != nil {
			return nil, projectionErr
		}
		running := false
		if subject, found := view.agents.Get(header.ID); found {
			running = subject.StatusValue() == agent.StatusRunning
		}
		items = append(items, summarizeSession(header, events, running, projectionSnapshot))
	}
	storedHeaders, err := view.persistence.List(requestContext)
	if err != nil {
		return nil, err
	}
	for _, header := range storedHeaders {
		if _, live := liveIDs[header.ID]; live || header.CWD == nil {
			continue
		}
		loaded, err := view.persistence.Inspect(requestContext, header.ID)
		if err != nil {
			return nil, err
		}
		restored, err := view.projections.Restore(sessionprojection.Checkpoint{}, loaded.Events, 0)
		if err != nil {
			return nil, err
		}
		items = append(items, summarizeSession(loaded.Header, loaded.Events, false, restored.Snapshot))
	}
	sort.SliceStable(items, func(leftIndex int, rightIndex int) bool {
		return items[leftIndex].UpdatedAt > items[rightIndex].UpdatedAt
	})
	if items == nil {
		items = []api.SessionSummary{}
	}
	return items, nil
}

// VisibleSessionIDs exposes the same authorization boundary as session.list
// without leaking its wire projection into the Session Search capability.
func (view *sessionReader) VisibleSessionIDs(requestContext context.Context) (map[session.SessionID]struct{}, error) {
	visible, err := view.visibleSessionSummaries(requestContext)
	if err != nil {
		return nil, err
	}
	identifiers := make(map[session.SessionID]struct{}, len(visible))
	for _, item := range visible {
		identifiers[session.SessionID(item.SessionID)] = struct{}{}
	}
	return identifiers, nil
}

func (view *sessionReader) History(requestContext context.Context, call api.Request[api.SessionHistoryRequest]) (api.Outcome[api.SessionHistoryValue], error) {
	identifier := session.SessionID(call.Payload.SessionID)
	conversation, found := view.sessions.Get(identifier)
	var events []session.Event
	if found {
		events = conversation.Events()
	} else {
		loaded, err := view.persistence.Inspect(requestContext, identifier)
		if err != nil {
			var missing *sesspersist.NotFoundError
			if errors.As(err, &missing) {
				return api.Fail[api.SessionHistoryValue](sessionNotFoundError(call.Payload.SessionID)), nil
			}
			return api.Outcome[api.SessionHistoryValue]{}, err
		}
		events = loaded.Events
	}
	maxMessages := defaultHistoryMessages
	if call.Payload.MaxMessages != nil {
		maxMessages = *call.Payload.MaxMessages
	}
	page, hasMore, err := historyPage(events, call.Payload.BeforeSeq, maxMessages)
	if err != nil {
		return api.Outcome[api.SessionHistoryValue]{}, err
	}
	value := api.SessionHistoryValue{Events: page, HasMore: hasMore}
	if call.Payload.BeforeSeq == nil {
		restored, restoreErr := view.projections.Restore(sessionprojection.Checkpoint{}, events, 0)
		if restoreErr != nil {
			return api.Outcome[api.SessionHistoryValue]{}, restoreErr
		}
		value.Projections = api.ProjectSessionProjections(restored.Snapshot)
	}
	return api.OK(value), nil
}

func summarizeSession(
	metadata session.Header,
	entries []session.Event,
	running bool,
	projectionSnapshot sessionprojection.Snapshot,
) api.SessionSummary {
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
	summary := api.SessionSummary{
		SessionID: api.SessionID(metadata.ID), UpdatedAt: updatedAt, Running: running, Blank: blank,
		Origin: string(metadata.Origin), CWD: cloneStringPointer(metadata.CWD),
		AgentPreset: cloneStringPointer(metadata.AgentPreset),
	}
	if metadata.ParentSession != nil {
		summary.ParentSessionID = api.SessionID(*metadata.ParentSession)
	}
	if len(projectionSnapshot.Values) != 0 {
		summary.Projections = api.ProjectSessionProjections(projectionSnapshot)
	}
	return summary
}

func historyPage(events []session.Event, beforeSeq *int64, maxMessages int64) ([]api.HistoryEntry, bool, error) {
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
	page := make([]api.HistoryEntry, 0, len(window))
	for _, committed := range window {
		if committed.Seq < cut {
			continue
		}
		projected, err := api.ProjectSessionEvent(committed)
		if err != nil {
			return nil, false, err
		}
		page = append(page, api.HistoryEntry{Event: projected})
	}
	if page == nil {
		page = []api.HistoryEntry{}
	}
	return page, cut > 0, nil
}

func directUserEvent(committed session.Event) bool {
	var envelope struct {
		Source struct {
			Kind string `json:"kind"`
		} `json:"source"`
	}
	return json.Unmarshal(committed.Data, &envelope) == nil && envelope.Source.Kind == "user"
}
