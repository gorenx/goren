package sessionapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/gorenx/goren/agent"
	api "github.com/gorenx/goren/apiproxy"
	"github.com/gorenx/goren/session"
	sesspersist "github.com/gorenx/goren/session/persistence"
	sessionprojection "github.com/gorenx/goren/session/projection"
)

const defaultHistoryMessages int64 = 50

const historyReadBatchEvents int64 = 512

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
	maxMessages := defaultHistoryMessages
	if call.Payload.MaxMessages != nil {
		maxMessages = *call.Payload.MaxMessages
	}
	var page []api.HistoryEntry
	var hasMore bool
	var err error
	if found {
		page, hasMore, err = historyPage(
			conversation.Events(),
			call.Payload.BeforeSeq,
			maxMessages,
		)
	} else {
		page, hasMore, err = view.coldHistoryPage(
			requestContext,
			identifier,
			call.Payload.BeforeSeq,
			maxMessages,
		)
	}
	if err != nil {
		var missing *sesspersist.NotFoundError
		if errors.As(err, &missing) {
			return api.Fail[api.SessionHistoryValue](sessionNotFoundError(call.Payload.SessionID)), nil
		}
		return api.Outcome[api.SessionHistoryValue]{}, err
	}
	value := api.SessionHistoryValue{Events: page, HasMore: hasMore}
	if call.Payload.BeforeSeq != nil {
		return api.OK(value), nil
	}
	projectionSnapshot, err := view.historyProjections(requestContext, identifier, conversation, found)
	if err != nil {
		return api.Outcome[api.SessionHistoryValue]{}, err
	}
	value.Projections = api.ProjectSessionProjections(projectionSnapshot)
	return api.OK(value), nil
}

func (view *sessionReader) historyProjections(
	requestContext context.Context,
	identifier session.SessionID,
	conversation session.Context,
	found bool,
) (sessionprojection.Snapshot, error) {
	if found {
		return view.projections.Snapshot(conversation)
	}
	loaded, err := view.persistence.Inspect(requestContext, identifier)
	if err != nil {
		return sessionprojection.Snapshot{}, err
	}
	restored, err := view.projections.Restore(
		sessionprojection.Checkpoint{},
		loaded.Events,
		0,
	)
	if err != nil {
		return sessionprojection.Snapshot{}, err
	}
	return restored.Snapshot, nil
}

func (view *sessionReader) coldHistoryPage(
	requestContext context.Context,
	identifier session.SessionID,
	beforeSeq *int64,
	maxMessages int64,
) ([]api.HistoryEntry, bool, error) {
	if maxMessages < 1 {
		return nil, false, errors.New("session.history: maxMessages must be positive")
	}
	cursor := cloneSequence(beforeSeq)
	entries := make([]session.Event, 0)
	messageCount := int64(0)
	var cut *int64
	for {
		window, err := view.persistence.ReadEventsBefore(
			requestContext,
			identifier,
			cursor,
			historyReadBatchEvents,
		)
		if err != nil {
			return nil, false, err
		}
		if len(window.Events) == 0 {
			break
		}
		if err := appendOlderHistoryWindow(&entries, window.Events); err != nil {
			return nil, false, err
		}
		if cut == nil {
			cut, messageCount = findHistoryCut(window.Events, maxMessages, messageCount)
		}
		if cut != nil && entries[len(entries)-1].Seq <= *cut {
			break
		}
		if !window.HasEarlier {
			break
		}
		cursor = sequencePointer(window.Events[len(window.Events)-1].Seq)
	}
	if cut != nil {
		entries = slices.DeleteFunc(entries, func(committed session.Event) bool {
			return committed.Seq < *cut
		})
	}
	slices.Reverse(entries)
	projected, err := projectHistoryEntries(entries)
	if err != nil {
		return nil, false, err
	}
	return projected, cut != nil && *cut > 0, nil
}

func cloneSequence(sequence *int64) *int64 {
	if sequence == nil {
		return nil
	}
	return sequencePointer(*sequence)
}

func sequencePointer(sequence int64) *int64 {
	return &sequence
}

func appendOlderHistoryWindow(destination *[]session.Event, older []session.Event) error {
	if len(*destination) != 0 && (*destination)[len(*destination)-1].Seq-1 != older[0].Seq {
		return fmt.Errorf(
			"session.history: discontinuous cold event windows at seq %d",
			older[0].Seq,
		)
	}
	*destination = append(*destination, older...)
	return nil
}

func findHistoryCut(
	entries []session.Event,
	maxMessages int64,
	messageCount int64,
) (*int64, int64) {
	for _, committed := range entries {
		groupStart, counted := historyMessageGroupStart(committed)
		if !counted {
			continue
		}
		messageCount++
		if messageCount >= maxMessages {
			return sequencePointer(groupStart), messageCount
		}
	}
	return nil, messageCount
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
		groupStart, counted := historyMessageGroupStart(committed)
		if !counted {
			continue
		}
		count++
		if count >= maxMessages {
			cut = groupStart
			break
		}
	}
	selected := make([]session.Event, 0, len(window))
	for _, committed := range window {
		if committed.Seq < cut {
			continue
		}
		selected = append(selected, committed)
	}
	page, err := projectHistoryEntries(selected)
	if err != nil {
		return nil, false, err
	}
	return page, cut > 0, nil
}

func historyMessageGroupStart(committed session.Event) (int64, bool) {
	if (committed.Type != session.UserMessageEventName && committed.Type != session.AssistantMessageEventName) ||
		committed.SurfaceOp == nil || committed.SurfaceOp.Kind != session.SurfaceOperationAppend {
		return 0, false
	}
	groupStart := committed.Seq
	if committed.SourceEventSeqs != nil {
		for _, sourceSeq := range *committed.SourceEventSeqs {
			if sourceSeq < groupStart {
				groupStart = sourceSeq
			}
		}
	}
	return groupStart, true
}

func projectHistoryEntries(events []session.Event) ([]api.HistoryEntry, error) {
	page := make([]api.HistoryEntry, 0, len(events))
	for _, committed := range events {
		projected, err := api.ProjectSessionEvent(committed)
		if err != nil {
			return nil, err
		}
		page = append(page, api.HistoryEntry{Event: projected})
	}
	return page, nil
}

func directUserEvent(committed session.Event) bool {
	var envelope struct {
		Source struct {
			Kind string `json:"kind"`
		} `json:"source"`
	}
	return json.Unmarshal(committed.Data, &envelope) == nil && envelope.Source.Kind == "user"
}
