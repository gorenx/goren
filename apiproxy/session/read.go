package sessionapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/gorenx/goren/agent"
	api "github.com/gorenx/goren/apiproxy"
	"github.com/gorenx/goren/session"
	sesspersist "github.com/gorenx/goren/session/persistence"
	sessproj "github.com/gorenx/goren/session/projection"
)

const defaultHistoryMessages int64 = 50

const (
	defaultSessionListPageSize int64 = 50
	maximumSessionListPageSize int64 = 100
)

const historyReadBatchEvents int64 = 512

type sessionReader struct {
	agents        agent.Registry
	sessions      session.LiveStore
	persistence   sesspersist.Persistence
	projections   sessproj.Registry
	cache         ProjectionCache
	reportFailure func(error)
}

type coldSessionProjection struct {
	snapshot sessproj.Snapshot
	metadata *sessionListMetadataState
}

type historyWindow struct {
	events  []api.HistoryEntry
	hasMore bool
}

type sessionSummaryPage struct {
	items      []api.SessionSummary
	nextCursor *sesspersist.SessionCursor
}

func (view *sessionReader) List(
	requestContext context.Context,
	call api.Request[api.SessionListRequest],
) (api.Outcome[api.SessionListValue], error) {
	page, err := decodeSessionListPage(call.Payload)
	if err != nil {
		return api.Outcome[api.SessionListValue]{}, err
	}
	summaries, err := view.visibleSessionSummaries(requestContext, page)
	if err != nil {
		return api.Outcome[api.SessionListValue]{}, err
	}
	nextCursor, err := encodeSessionListCursor(summaries.nextCursor)
	if err != nil {
		return api.Outcome[api.SessionListValue]{}, err
	}
	return api.OK(api.SessionListValue{
		Items:      summaries.items,
		NextCursor: nextCursor,
	}), nil
}

func (view *sessionReader) visibleSessionSummaries(
	requestContext context.Context,
	page sesspersist.SessionPage,
) (sessionSummaryPage, error) {
	liveItems, liveIDs := view.liveSessionSummaries()
	items := make([]api.SessionSummary, 0, len(liveItems)+int(page.Limit))
	if page.Cursor == nil {
		items = append(items, liveItems...)
	}
	stored, err := view.persistence.List(requestContext, page)
	if err != nil {
		return sessionSummaryPage{}, err
	}
	for _, header := range stored.Headers {
		if header.CWD == nil {
			continue
		}
		if _, live := liveIDs[header.ID]; live {
			continue
		}
		items = append(items, view.storedSessionSummary(requestContext, header))
	}
	return sessionSummaryPage{
		items:      items,
		nextCursor: stored.NextCursor,
	}, nil
}

func (view *sessionReader) liveSessionSummaries() (
	[]api.SessionSummary,
	map[session.SessionID]struct{},
) {
	items := make([]api.SessionSummary, 0)
	identifiers := make(map[session.SessionID]struct{})
	for _, conversation := range view.sessions.List() {
		header := conversation.Header()
		if header.CWD == nil {
			continue
		}
		identifiers[header.ID] = struct{}{}
		items = append(items, view.summarizeLiveSession(conversation))
	}
	return items, identifiers
}

func (view *sessionReader) storedSessionSummary(
	requestContext context.Context,
	header session.Header,
) api.SessionSummary {
	projectionState := view.coldSessionProjection(requestContext, header)
	if conversation, live := view.sessions.Get(header.ID); live {
		return view.summarizeLiveSession(conversation)
	}
	return summarizeColdSession(header, projectionState)
}

func (view *sessionReader) coldSessionProjection(
	requestContext context.Context,
	header session.Header,
) coldSessionProjection {
	if view.cache == nil {
		return coldSessionProjection{}
	}
	projectionSnapshot, err := view.cache.CachedSnapshot(header)
	if err != nil {
		view.reportReadProblem(fmt.Errorf(
			"apiproxy/session: read cached summary for %q: %w",
			header.ID,
			err,
		))
		return coldSessionProjection{}
	}
	if projectionSnapshot == nil {
		restored, restoreErr := view.cache.ColdSnapshot(requestContext, header.ID)
		if restoreErr != nil {
			view.reportReadProblem(fmt.Errorf(
				"apiproxy/session: restore cold summary for %q: %w",
				header.ID,
				restoreErr,
			))
			return coldSessionProjection{}
		}
		projectionSnapshot = &restored
	}
	result := coldSessionProjection{
		snapshot: *projectionSnapshot,
	}
	metadata, found, err := readSessionListMetadata(projectionSnapshot.Values)
	if err != nil {
		view.reportReadProblem(err)
		return result
	}
	if found {
		result.metadata = &metadata
	}
	return result
}

// VisibleSessionIDs exposes the same authorization boundary as session.list
// without leaking its wire projection into the Session Search capability.
func (view *sessionReader) VisibleSessionIDs(requestContext context.Context) (map[session.SessionID]struct{}, error) {
	identifiers := make(map[session.SessionID]struct{})
	var cursor *sesspersist.SessionCursor
	for {
		visible, err := view.visibleSessionSummaries(
			requestContext,
			sesspersist.SessionPage{
				Cursor: cursor,
				Limit:  maximumSessionListPageSize,
			},
		)
		if err != nil {
			return nil, err
		}
		for _, item := range visible.items {
			identifiers[session.SessionID(item.SessionID)] = struct{}{}
		}
		if visible.nextCursor == nil {
			return identifiers, nil
		}
		cursor = visible.nextCursor
	}
}

func (view *sessionReader) History(
	requestContext context.Context,
	call api.Request[api.SessionHistoryRequest],
) (api.Outcome[api.SessionHistoryValue], error) {
	identifier := session.SessionID(call.Payload.SessionID)
	maxMessages, err := historyMessageLimit(call.Payload.MaxMessages)
	if err != nil {
		return api.Outcome[api.SessionHistoryValue]{}, err
	}
	value, err := view.readHistory(
		requestContext,
		identifier,
		call.Payload.BeforeSeq,
		maxMessages,
	)
	if err != nil {
		var missing *sesspersist.NotFoundError
		if errors.As(err, &missing) {
			return api.Fail[api.SessionHistoryValue](sessionNotFoundError(call.Payload.SessionID)), nil
		}
		return api.Outcome[api.SessionHistoryValue]{}, err
	}
	return api.OK(value), nil
}

func historyMessageLimit(configured *int64) (int64, error) {
	if configured == nil {
		return defaultHistoryMessages, nil
	}
	if *configured < 1 {
		return 0, errors.New("session.history: maxMessages must be positive")
	}
	return *configured, nil
}

func (view *sessionReader) readHistory(
	requestContext context.Context,
	identifier session.SessionID,
	beforeSeq *int64,
	maxMessages int64,
) (api.SessionHistoryValue, error) {
	conversation, live := view.sessions.Get(identifier)
	if beforeSeq != nil {
		window, err := view.historyBefore(
			requestContext,
			identifier,
			conversation,
			live,
			beforeSeq,
			maxMessages,
		)
		return historyValue(window, nil), err
	}
	if live {
		return view.liveHistoryTail(conversation, maxMessages)
	}
	if view.cache != nil {
		return view.coldHistoryTail(requestContext, identifier, maxMessages)
	}
	window, err := view.coldHistoryPage(
		requestContext,
		identifier,
		nil,
		maxMessages,
	)
	return historyValue(window, nil), err
}

func (view *sessionReader) historyBefore(
	requestContext context.Context,
	identifier session.SessionID,
	conversation session.Context,
	live bool,
	beforeSeq *int64,
	maxMessages int64,
) (historyWindow, error) {
	if live {
		return historyPage(conversation.Events(), beforeSeq, maxMessages)
	}
	return view.coldHistoryPage(
		requestContext,
		identifier,
		beforeSeq,
		maxMessages,
	)
}

func (view *sessionReader) liveHistoryTail(
	conversation session.Context,
	maxMessages int64,
) (api.SessionHistoryValue, error) {
	projectionSnapshot, err := view.projections.Snapshot(conversation)
	if err != nil {
		return api.SessionHistoryValue{}, err
	}
	cursor := projectionSnapshot.AsOfSeq + 1
	window, err := historyPage(conversation.Events(), &cursor, maxMessages)
	return historyValue(window, &projectionSnapshot), err
}

func (view *sessionReader) coldHistoryTail(
	requestContext context.Context,
	identifier session.SessionID,
	maxMessages int64,
) (api.SessionHistoryValue, error) {
	for attempt := 0; attempt < 3; attempt++ {
		snapshot, err := view.cache.ColdSnapshot(requestContext, identifier)
		if err != nil {
			return api.SessionHistoryValue{}, err
		}
		cursor := snapshot.AsOfSeq + 1
		window, err := view.coldHistoryPage(
			requestContext,
			identifier,
			&cursor,
			maxMessages,
		)
		if err != nil {
			return api.SessionHistoryValue{}, err
		}
		if historyPageMatchesCut(window.events, snapshot.AsOfSeq) {
			return historyValue(window, &snapshot), nil
		}
	}
	return api.SessionHistoryValue{}, errors.New(
		"session.history: durable log did not stabilize at the projection cut",
	)
}

func (view *sessionReader) coldHistoryPage(
	requestContext context.Context,
	identifier session.SessionID,
	beforeSeq *int64,
	maxMessages int64,
) (historyWindow, error) {
	if maxMessages < 1 {
		return historyWindow{}, errors.New("session.history: maxMessages must be positive")
	}
	cursor := cloneSequence(beforeSeq)
	entries := make([]session.Event, 0)
	messageCount := int64(0)
	var cut *int64
	for {
		window, err := view.persistence.ReadEventsBefore(
			requestContext,
			identifier,
			sesspersist.EventPage{
				BeforeSeq: cursor,
				Limit:     historyReadBatchEvents,
			},
		)
		if err != nil {
			return historyWindow{}, err
		}
		if len(window.Events) == 0 {
			break
		}
		if err := appendOlderHistoryWindow(&entries, window.Events); err != nil {
			return historyWindow{}, err
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
		return historyWindow{}, err
	}
	return historyWindow{
		events:  projected,
		hasMore: cut != nil && *cut > 0,
	}, nil
}

func historyValue(
	window historyWindow,
	projectionSnapshot *sessproj.Snapshot,
) api.SessionHistoryValue {
	value := api.SessionHistoryValue{
		Events:  window.events,
		HasMore: window.hasMore,
	}
	if projectionSnapshot != nil {
		value.Projections = api.ProjectSessionProjections(*projectionSnapshot)
	}
	return value
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
	projectionSnapshot sessproj.Snapshot,
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

func (view *sessionReader) summarizeLiveSession(
	conversation session.Context,
) api.SessionSummary {
	metadata := conversation.Header()
	snapshot, err := view.projections.Snapshot(conversation)
	if err != nil {
		view.reportReadProblem(fmt.Errorf(
			"apiproxy/session: read live summary projection for %q: %w",
			metadata.ID,
			err,
		))
	}
	running := false
	if subject, found := view.agents.Get(metadata.ID); found {
		running = subject.StatusValue() == agent.StatusRunning
	}
	return summarizeSession(
		metadata,
		conversation.Events(),
		running,
		snapshot,
	)
}

func summarizeColdSession(
	metadata session.Header,
	projectionState coldSessionProjection,
) api.SessionSummary {
	updatedAt := metadata.CreatedAt
	if projectionState.metadata != nil &&
		projectionState.metadata.LastPromptAt != nil &&
		*projectionState.metadata.LastPromptAt > updatedAt {
		updatedAt = *projectionState.metadata.LastPromptAt
	}
	summary := api.SessionSummary{
		SessionID:   api.SessionID(metadata.ID),
		UpdatedAt:   updatedAt,
		Running:     false,
		Blank:       false,
		Origin:      string(metadata.Origin),
		CWD:         cloneStringPointer(metadata.CWD),
		AgentPreset: cloneStringPointer(metadata.AgentPreset),
	}
	if metadata.ParentSession != nil {
		summary.ParentSessionID = api.SessionID(*metadata.ParentSession)
	}
	if len(projectionState.snapshot.Values) != 0 {
		summary.Projections = api.ProjectSessionProjections(projectionState.snapshot)
	}
	return summary
}

func historyPageMatchesCut(page []api.HistoryEntry, cut int64) bool {
	if cut < 0 {
		return len(page) == 0
	}
	return len(page) != 0 && page[len(page)-1].Event.Seq == cut
}

func (view *sessionReader) reportReadProblem(problem error) {
	if problem == nil || view.reportFailure == nil {
		return
	}
	defer func() { _ = recover() }()
	view.reportFailure(problem)
}

func historyPage(
	events []session.Event,
	beforeSeq *int64,
	maxMessages int64,
) (historyWindow, error) {
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
		return historyWindow{}, err
	}
	return historyWindow{
		events:  page,
		hasMore: cut > 0,
	}, nil
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
