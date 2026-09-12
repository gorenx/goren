package sessionapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	rpc "github.com/gorenx/goren/apiproxy/rpc"
	sessionframes "github.com/gorenx/goren/apiproxy/session/events"
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
	sessions      LiveSessions
	persistence   History
	cache         ProjectionCache
	reportFailure func(error)
}

type coldSessionProjection struct {
	snapshot sessproj.Snapshot
	metadata *sessionListMetadataState
}

type historyWindow struct {
	events  []HistoryEntry
	hasMore bool
}

type sessionSummaryPage struct {
	items      []Summary
	nextCursor *sesspersist.SessionCursor
}

func (view *sessionReader) List(
	requestContext context.Context,
	call rpc.Request[ListRequest],
) (rpc.Outcome[ListValue], error) {
	page, err := decodeSessionListPage(call.Payload)
	if err != nil {
		return rpc.Outcome[ListValue]{}, err
	}
	summaries, err := view.visibleSessionSummaries(requestContext, page)
	if err != nil {
		return rpc.Outcome[ListValue]{}, err
	}
	nextCursor, err := encodeSessionListCursor(summaries.nextCursor)
	if err != nil {
		return rpc.Outcome[ListValue]{}, err
	}
	return rpc.OK(ListValue{
		Items:      summaries.items,
		NextCursor: nextCursor,
	}), nil
}

func (view *sessionReader) visibleSessionSummaries(
	requestContext context.Context,
	page sesspersist.SessionPage,
) (sessionSummaryPage, error) {
	liveItems, liveIDs, err := view.liveSessionSummaries(requestContext)
	if err != nil {
		return sessionSummaryPage{}, err
	}
	items := make([]Summary, 0, len(liveItems)+int(page.Limit))
	if page.Cursor == nil {
		items = append(items, liveItems...)
	}
	stored, err := view.persistence.List(requestContext, page)
	if err != nil {
		return sessionSummaryPage{}, err
	}
	for _, header := range stored.Headers {
		// Child Sessions remain navigable through their parent without a workspace.
		if header.CWD == nil && header.ParentSession == nil {
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

func (view *sessionReader) liveSessionSummaries(ctx context.Context) (
	[]Summary,
	map[session.SessionID]struct{},
	error,
) {
	items := make([]Summary, 0)
	identifiers := make(map[session.SessionID]struct{})
	snapshots, err := view.sessions.List(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, conversation := range snapshots {
		header := conversation.Header
		// Child Sessions remain navigable through their parent without a workspace.
		if header.CWD == nil && header.ParentSession == nil {
			continue
		}
		identifiers[header.ID] = struct{}{}
		items = append(items, view.summarizeLiveSession(conversation))
	}
	return items, identifiers, nil
}

func (view *sessionReader) storedSessionSummary(
	requestContext context.Context,
	header session.Header,
) Summary {
	projectionState := view.coldSessionProjection(requestContext, header)
	return summarizeColdSession(header, projectionState)
}

func (view *sessionReader) coldSessionProjection(
	requestContext context.Context,
	header session.Header,
) coldSessionProjection {
	if view.cache == nil {
		return coldSessionProjection{}
	}
	projectionSnapshot, err := view.cache.CachedSnapshot(requestContext, header)
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
	call rpc.Request[HistoryRequest],
) (rpc.Outcome[HistoryValue], error) {
	identifier := session.SessionID(call.Payload.SessionID)
	maxMessages, err := historyMessageLimit(call.Payload.MaxMessages)
	if err != nil {
		return rpc.Outcome[HistoryValue]{}, err
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
			return rpc.Fail[HistoryValue](sessionNotFoundError(call.Payload.SessionID)), nil
		}
		return rpc.Outcome[HistoryValue]{}, err
	}
	return rpc.OK(value), nil
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
) (HistoryValue, error) {
	conversation, err := view.sessions.Read(requestContext, identifier)
	if err != nil {
		return HistoryValue{}, err
	}
	live := conversation != nil
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
	conversation *Snapshot,
	live bool,
	beforeSeq *int64,
	maxMessages int64,
) (historyWindow, error) {
	if live {
		return historyPage(conversation.Events, beforeSeq, maxMessages)
	}
	return view.coldHistoryPage(
		requestContext,
		identifier,
		beforeSeq,
		maxMessages,
	)
}

func (view *sessionReader) liveHistoryTail(
	conversation *Snapshot,
	maxMessages int64,
) (HistoryValue, error) {
	projectionSnapshot := conversation.Projections
	cursor := projectionSnapshot.AsOfSeq + 1
	window, err := historyPage(conversation.Events, &cursor, maxMessages)
	return historyValue(window, &projectionSnapshot), err
}

func (view *sessionReader) coldHistoryTail(
	requestContext context.Context,
	identifier session.SessionID,
	maxMessages int64,
) (HistoryValue, error) {
	for attempt := 0; attempt < 3; attempt++ {
		snapshot, err := view.cache.ColdSnapshot(requestContext, identifier)
		if err != nil {
			return HistoryValue{}, err
		}
		cursor := snapshot.AsOfSeq + 1
		window, err := view.coldHistoryPage(
			requestContext,
			identifier,
			&cursor,
			maxMessages,
		)
		if err != nil {
			return HistoryValue{}, err
		}
		if historyPageMatchesCut(window.events, snapshot.AsOfSeq) {
			return historyValue(window, &snapshot), nil
		}
	}
	return HistoryValue{}, errors.New(
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
) HistoryValue {
	value := HistoryValue{
		Events:  window.events,
		HasMore: window.hasMore,
	}
	if projectionSnapshot != nil {
		value.Projections = sessionframes.ProjectProjections(*projectionSnapshot)
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
) Summary {
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
	summary := Summary{
		SessionID:   sessionframes.SessionID(metadata.ID),
		UpdatedAt:   updatedAt,
		Running:     running,
		Blank:       blank,
		Origin:      string(metadata.Origin),
		CWD:         cloneStringPointer(metadata.CWD),
		AgentPreset: cloneStringPointer(metadata.AgentPreset),
	}
	if metadata.ParentSession != nil {
		summary.ParentSessionID = sessionframes.SessionID(*metadata.ParentSession)
	}
	if len(projectionSnapshot.Values) != 0 {
		summary.Projections = sessionframes.ProjectProjections(projectionSnapshot)
	}
	return summary
}

func (view *sessionReader) summarizeLiveSession(conversation Snapshot) Summary {
	return summarizeSession(conversation.Header, conversation.Events, conversation.Running, conversation.Projections)
}

func summarizeColdSession(
	metadata session.Header,
	projectionState coldSessionProjection,
) Summary {
	updatedAt := metadata.CreatedAt
	if projectionState.metadata != nil &&
		projectionState.metadata.LastPromptAt != nil &&
		*projectionState.metadata.LastPromptAt > updatedAt {
		updatedAt = *projectionState.metadata.LastPromptAt
	}
	summary := Summary{
		SessionID:   sessionframes.SessionID(metadata.ID),
		UpdatedAt:   updatedAt,
		Running:     false,
		Blank:       false,
		Origin:      string(metadata.Origin),
		CWD:         cloneStringPointer(metadata.CWD),
		AgentPreset: cloneStringPointer(metadata.AgentPreset),
	}
	if metadata.ParentSession != nil {
		summary.ParentSessionID = sessionframes.SessionID(*metadata.ParentSession)
	}
	if len(projectionState.snapshot.Values) != 0 {
		summary.Projections = sessionframes.ProjectProjections(projectionState.snapshot)
	}
	return summary
}

func historyPageMatchesCut(page []HistoryEntry, cut int64) bool {
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

func projectHistoryEntries(events []session.Event) ([]HistoryEntry, error) {
	page := make([]HistoryEntry, 0, len(events))
	for _, committed := range events {
		projected, err := sessionframes.ProjectEvent(committed)
		if err != nil {
			return nil, err
		}
		page = append(page, HistoryEntry{
			Event: projected,
		})
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

// ReadEvents returns one identity-checked live or durable Session snapshot.
// It performs no activation and never falls back after a live read failure.
func (view *sessionReader) ReadEvents(ctx context.Context, id session.SessionID) ([]session.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snapshot, err := view.sessions.Read(ctx, id)
	if err != nil {
		return nil, err
	}
	if snapshot != nil {
		if snapshot.Header.ID != id {
			return nil, errors.New("session identity mismatch")
		}
		return snapshot.Events, nil
	}
	archive, err := view.persistence.Inspect(ctx, id)
	if err != nil {
		return nil, err
	}
	if archive.Header.ID != id {
		return nil, errors.New("session identity mismatch")
	}
	return archive.Events, nil
}
