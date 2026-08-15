package query

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/gorenx/goren/session"
	sesspersist "github.com/gorenx/goren/session/persistence"
	sessiontitle "github.com/gorenx/goren/session/title"
)

// ListSessions returns one detached live-preferred observation ordered newest
// first, with source availability retained separately from source choice.
func (owner *Service) ListSessions(requestContext context.Context) ([]SessionRecord, error) {
	if err := contextFailure(requestContext); err != nil {
		return nil, err
	}
	byID := make(map[session.SessionID]SessionRecord)
	if owner.persistence != nil {
		snapshots, err := owner.persistence.ListSnapshots(requestContext)
		if err != nil {
			return nil, persistenceFailure("list snapshots", err)
		}
		for _, snapshot := range snapshots {
			if _, duplicate := byID[snapshot.Header.ID]; duplicate {
				return nil, failure(
					ErrorSourceConflict,
					fmt.Sprintf("session query: persistence listed session %q more than once", snapshot.Header.ID),
					nil,
				)
			}
			byID[snapshot.Header.ID] = SessionRecord{Header: cloneHeader(snapshot.Header), Persisted: true}
		}
	}
	for _, conversation := range owner.sessions.List() {
		metadata := conversation.Header()
		persisted, found := byID[metadata.ID]
		if found && !headersEqual(metadata, persisted.Header) {
			return nil, sourceConflict(metadata, persisted.Header)
		}
		byID[metadata.ID] = SessionRecord{
			Header: cloneHeader(metadata), Live: true, Persisted: found,
		}
	}
	result := make([]SessionRecord, 0, len(byID))
	for _, candidate := range byID {
		result = append(result, candidate)
	}
	sort.Slice(result, func(leftPosition int, rightPosition int) bool {
		left := result[leftPosition].Header
		right := result[rightPosition].Header
		return left.CreatedAt > right.CreatedAt || left.CreatedAt == right.CreatedAt && left.ID < right.ID
	})
	if result == nil {
		result = []SessionRecord{}
	}
	return result, contextFailure(requestContext)
}

// ReadSession returns one complete detached log without publishing a cold
// Session into the live Store.
func (owner *Service) ReadSession(
	requestContext context.Context,
	identifier session.SessionID,
) (LogSnapshot, error) {
	loaded, err := owner.loadLogical(requestContext, identifier)
	if err != nil {
		return LogSnapshot{}, err
	}
	if err := validateLogical(loaded.Header, loaded.Events); err != nil {
		return LogSnapshot{}, failure(
			ErrorCorruptSession,
			fmt.Sprintf("session query: session %q failed replay validation", identifier),
			err,
		)
	}
	return loaded, nil
}

// FilterSessions applies provider-independent metadata predicates to one
// complete logical-corpus listing.
func (owner *Service) FilterSessions(
	requestContext context.Context,
	constraints SessionConstraints,
) ([]SessionRecord, error) {
	normalized, err := normalizeSessionConstraints(constraints)
	if err != nil {
		return nil, err
	}
	candidates, err := owner.ListSessions(requestContext)
	if err != nil {
		return nil, err
	}
	result := make([]SessionRecord, 0, len(candidates))
	for _, candidate := range candidates {
		if sessionMatches(candidate, normalized) {
			result = append(result, candidate)
		}
	}
	return result, nil
}

// ReadTitle returns the latest log-backed title from one logical observation.
func (owner *Service) ReadTitle(
	requestContext context.Context,
	identifier session.SessionID,
) (*sessiontitle.Snapshot, error) {
	observation, err := owner.ReadTitleSnapshot(requestContext, identifier)
	if err != nil {
		return nil, err
	}
	return observation.Title, nil
}

// ReadTitleSnapshot binds a title fold to its exact source header.
func (owner *Service) ReadTitleSnapshot(
	requestContext context.Context,
	identifier session.SessionID,
) (TitleObservation, error) {
	results, err := owner.ReadTitleSnapshots(requestContext, []session.SessionID{identifier})
	if err != nil {
		return TitleObservation{}, err
	}
	if len(results) != 1 {
		return TitleObservation{}, errors.New("session query: title observation result is missing")
	}
	if results[0].Err != nil {
		return TitleObservation{}, results[0].Err
	}
	if results[0].Observation == nil {
		return TitleObservation{}, errors.New("session query: title observation is empty")
	}
	return *results[0].Observation, nil
}

// ReadTitleSnapshots folds unique ids with bounded persisted-log inspection
// concurrency. Per-Session failures remain isolated; cancellation aborts the
// entire batch after started inspections settle.
func (owner *Service) ReadTitleSnapshots(
	requestContext context.Context,
	identifiers []session.SessionID,
) ([]TitleObservationResult, error) {
	if err := contextFailure(requestContext); err != nil {
		return nil, err
	}
	unique := make([]session.SessionID, 0, len(identifiers))
	seen := make(map[session.SessionID]struct{}, len(identifiers))
	for _, identifier := range identifiers {
		if _, duplicate := seen[identifier]; duplicate {
			continue
		}
		seen[identifier] = struct{}{}
		unique = append(unique, identifier)
	}
	results := make([]TitleObservationResult, len(unique))
	unresolved := make([]int, 0)
	for position, identifier := range unique {
		results[position].SessionID = identifier
		if conversation, found := owner.sessions.Get(identifier); found {
			observation, err := foldTitle(conversation.Header(), conversation.Events())
			results[position].Observation = observation
			results[position].Err = err
		} else {
			unresolved = append(unresolved, position)
		}
	}
	if len(unresolved) == 0 {
		return results, nil
	}
	if owner.persistence == nil {
		for _, position := range unresolved {
			identifier := unique[position]
			results[position].Err = sessionNotFound(identifier)
		}
		return results, nil
	}
	snapshots, err := owner.persistence.ListSnapshots(requestContext)
	if err != nil {
		return nil, persistenceFailure("list snapshots", err)
	}
	listed := make(map[session.SessionID]session.Header, len(snapshots))
	for _, snapshot := range snapshots {
		listed[snapshot.Header.ID] = cloneHeader(snapshot.Header)
	}
	jobs := make(chan int)
	var workers sync.WaitGroup
	workerCount := min(owner.settings.PersistedInspectConcurrency, len(unresolved))
	workers.Add(workerCount)
	for workerPosition := 0; workerPosition < workerCount; workerPosition++ {
		go func() {
			defer workers.Done()
			for position := range jobs {
				if requestContext.Err() != nil {
					continue
				}
				identifier := unique[position]
				listedHeader, found := listed[identifier]
				if !found {
					if conversation, live := owner.sessions.Get(identifier); live {
						observation, foldErr := foldTitle(conversation.Header(), conversation.Events())
						results[position].Observation = observation
						results[position].Err = foldErr
					} else {
						results[position].Err = sessionNotFound(identifier)
					}
					continue
				}
				loaded, inspectErr := owner.persistence.Inspect(requestContext, identifier)
				if inspectErr != nil {
					results[position].Err = persistenceFailure("inspect", inspectErr)
					continue
				}
				if conversation, live := owner.sessions.Get(identifier); live {
					observation, foldErr := foldTitle(conversation.Header(), conversation.Events())
					results[position].Observation = observation
					results[position].Err = foldErr
					continue
				}
				if !headersEqual(listedHeader, loaded.Header) {
					results[position].Err = sourceConflict(listedHeader, loaded.Header)
					continue
				}
				observation, foldErr := foldTitle(loaded.Header, loaded.Events)
				results[position].Observation = observation
				results[position].Err = foldErr
			}
		}()
	}
	for _, position := range unresolved {
		if requestContext.Err() != nil {
			break
		}
		jobs <- position
	}
	close(jobs)
	workers.Wait()
	if err := contextFailure(requestContext); err != nil {
		return nil, err
	}
	return results, nil
}

// ListEvents classifies every raw event against one folded surface.
func (owner *Service) ListEvents(
	requestContext context.Context,
	identifier session.SessionID,
) ([]EventRecord, error) {
	loaded, err := owner.loadLogical(requestContext, identifier)
	if err != nil {
		return nil, err
	}
	analysis, err := analyzeLog(loaded.Header, loaded.Events)
	if err != nil {
		return nil, err
	}
	return analysis.records, nil
}

// FilterEvents scans extracted semantic documents with metadata predicates and
// an optional literal, case-insensitive, whitespace-flexible text predicate.
func (owner *Service) FilterEvents(
	requestContext context.Context,
	identifier session.SessionID,
	constraints EventConstraints,
	textValue string,
) ([]Document, error) {
	normalized, err := normalizeEventConstraints(constraints)
	if err != nil {
		return nil, err
	}
	loaded, err := owner.loadLogical(requestContext, identifier)
	if err != nil {
		return nil, err
	}
	documents, err := BuildDocuments(identifier, loaded.Events)
	if err != nil {
		return nil, err
	}
	var pattern *regexp.Regexp
	if strings.TrimSpace(textValue) != "" {
		parts := strings.Fields(textValue)
		quoted := make([]string, len(parts))
		for position, part := range parts {
			quoted[position] = regexp.QuoteMeta(part)
		}
		pattern, err = regexp.Compile("(?i)" + strings.Join(quoted, `\s+`))
		if err != nil {
			return nil, failure(ErrorInvalidFilter, "session query: invalid text filter", err)
		}
	}
	result := make([]Document, 0, len(documents))
	for _, entry := range documents {
		if eventMatches(entry, normalized) && (pattern == nil || pattern.MatchString(entry.Text)) {
			result = append(result, entry)
		}
	}
	return result, nil
}

// ReadSurface returns the current model-visible event sequence and raw capture
// boundary from one source observation.
func (owner *Service) ReadSurface(
	requestContext context.Context,
	identifier session.SessionID,
) (SurfaceSnapshot, error) {
	loaded, err := owner.loadLogical(requestContext, identifier)
	if err != nil {
		return SurfaceSnapshot{}, err
	}
	analysis, err := analyzeLog(loaded.Header, loaded.Events)
	if err != nil {
		return SurfaceSnapshot{}, err
	}
	result := SurfaceSnapshot{Header: loaded.Header, Events: make([]session.Event, 0, len(analysis.current))}
	if len(loaded.Events) != 0 {
		boundary := loaded.Events[len(loaded.Events)-1].Seq
		result.CapturedThroughSeq = &boundary
	}
	for _, sequence := range analysis.current {
		result.Events = append(result.Events, cloneEvent(loaded.Events[sequence]))
	}
	return result, nil
}

// ReadEvent returns one target plus a bounded contiguous raw-log window.
func (owner *Service) ReadEvent(
	requestContext context.Context,
	criteria EventReadRequest,
) (EventWindow, error) {
	if criteria.Before < 0 || criteria.After < 0 ||
		criteria.Before > *owner.settings.ReadWindowMax || criteria.After > *owner.settings.ReadWindowMax {
		return EventWindow{}, failure(
			ErrorInvalidWindow,
			fmt.Sprintf("session query: before and after must be between 0 and %d", *owner.settings.ReadWindowMax),
			nil,
		)
	}
	loaded, err := owner.loadLogical(requestContext, criteria.SessionID)
	if err != nil {
		return EventWindow{}, err
	}
	if criteria.Seq < 0 || criteria.Seq >= int64(len(loaded.Events)) || loaded.Events[criteria.Seq].Seq != criteria.Seq {
		return EventWindow{}, eventNotFound(criteria.SessionID, criteria.Seq)
	}
	start := max(int64(0), criteria.Seq-int64(criteria.Before))
	end := min(int64(len(loaded.Events)-1), criteria.Seq+int64(criteria.After))
	return EventWindow{
		Header: loaded.Header, Target: cloneEvent(loaded.Events[criteria.Seq]),
		Events: cloneEvents(loaded.Events[start : end+1]), StartSeq: start, EndSeq: end,
	}, nil
}

// TraceSession returns explicit parent ancestry and recursively known children
// from one logical-corpus observation.
func (owner *Service) TraceSession(
	requestContext context.Context,
	identifier session.SessionID,
) (LineageTrace, error) {
	candidates, err := owner.ListSessions(requestContext)
	if err != nil {
		return LineageTrace{}, err
	}
	byID := make(map[session.SessionID]SessionRecord, len(candidates))
	for _, candidate := range candidates {
		byID[candidate.Header.ID] = candidate
	}
	target, found := byID[identifier]
	if !found {
		return LineageTrace{}, sessionNotFound(identifier)
	}
	result := LineageTrace{Target: cloneRecord(target), Complete: true}
	ancestrySeen := map[session.SessionID]struct{}{identifier: {}}
	parent := target.Header.ParentSession
	for parent != nil {
		if _, duplicate := ancestrySeen[*parent]; duplicate {
			return LineageTrace{}, failure(
				ErrorInvalidLineage,
				fmt.Sprintf("session query: Session lineage contains a cycle at %q", *parent),
				nil,
			)
		}
		ancestrySeen[*parent] = struct{}{}
		ancestor, available := byID[*parent]
		if !available {
			unresolved := *parent
			result.Complete = false
			result.UnresolvedParent = &unresolved
			break
		}
		result.Ancestors = append(result.Ancestors, cloneRecord(ancestor))
		parent = ancestor.Header.ParentSession
	}
	children := make(map[session.SessionID][]SessionRecord)
	for _, candidate := range candidates {
		if candidate.Header.ParentSession != nil {
			parentID := *candidate.Header.ParentSession
			children[parentID] = append(children[parentID], candidate)
		}
	}
	for parentID := range children {
		sort.Slice(children[parentID], func(leftPosition int, rightPosition int) bool {
			left := children[parentID][leftPosition].Header
			right := children[parentID][rightPosition].Header
			return left.CreatedAt < right.CreatedAt || left.CreatedAt == right.CreatedAt && left.ID < right.ID
		})
	}
	result.Descendants, err = buildLineageChildren(children, identifier, map[session.SessionID]struct{}{identifier: {}})
	if err != nil {
		return LineageTrace{}, err
	}
	if result.Complete {
		root := target
		if len(result.Ancestors) != 0 {
			root = result.Ancestors[len(result.Ancestors)-1]
		}
		root = cloneRecord(root)
		result.Root = &root
	}
	return result, nil
}

// TraceEvent returns explicit surface replacement and source-event edges.
func (owner *Service) TraceEvent(
	requestContext context.Context,
	criteria EventTraceRequest,
) (EventTrace, error) {
	loaded, err := owner.loadLogical(requestContext, criteria.SessionID)
	if err != nil {
		return EventTrace{}, err
	}
	if criteria.Seq < 0 || criteria.Seq >= int64(len(loaded.Events)) || loaded.Events[criteria.Seq].Seq != criteria.Seq {
		return EventTrace{}, eventNotFound(criteria.SessionID, criteria.Seq)
	}
	analysis, err := analyzeLog(loaded.Header, loaded.Events)
	if err != nil {
		return EventTrace{}, err
	}
	result := EventTrace{
		Header: loaded.Header, Target: analysis.records[criteria.Seq],
		ReplacedEventSeqs: append([]int64{}, analysis.replacedEvents[criteria.Seq]...),
		SourceEventSeqs:   sourceSequences(loaded.Events[criteria.Seq]),
	}
	if replacementSeq, replaced := analysis.replacedBy[criteria.Seq]; replaced {
		copyValue := replacementSeq
		result.ReplacedBy = &copyValue
	}
	for next, replaced := analysis.replacedBy[criteria.Seq]; replaced; next, replaced = analysis.replacedBy[next] {
		result.ReplacementChain = append(result.ReplacementChain, next)
	}
	for _, entry := range loaded.Events {
		if entry.Seq > criteria.Seq && slices.Contains(sourceSequences(entry), criteria.Seq) {
			result.DerivedEventSeqs = append(result.DerivedEventSeqs, entry.Seq)
		}
	}
	return result, nil
}

func (owner *Service) loadLogical(
	requestContext context.Context,
	identifier session.SessionID,
) (LogSnapshot, error) {
	if err := contextFailure(requestContext); err != nil {
		return LogSnapshot{}, err
	}
	if conversation, found := owner.sessions.Get(identifier); found {
		return LogSnapshot{Header: conversation.Header(), Events: conversation.Events()}, nil
	}
	if owner.persistence == nil {
		return LogSnapshot{}, sessionNotFound(identifier)
	}
	snapshots, err := owner.persistence.ListSnapshots(requestContext)
	if err != nil {
		return LogSnapshot{}, persistenceFailure("list snapshots", err)
	}
	var listed *session.Header
	for _, snapshot := range snapshots {
		if snapshot.Header.ID == identifier {
			metadata := cloneHeader(snapshot.Header)
			listed = &metadata
			break
		}
	}
	if listed == nil {
		if conversation, found := owner.sessions.Get(identifier); found {
			return LogSnapshot{Header: conversation.Header(), Events: conversation.Events()}, nil
		}
		return LogSnapshot{}, sessionNotFound(identifier)
	}
	loaded, err := owner.persistence.Inspect(requestContext, identifier)
	if err != nil {
		var missing *sesspersist.NotFoundError
		if errors.As(err, &missing) {
			return LogSnapshot{}, sessionNotFound(identifier)
		}
		return LogSnapshot{}, persistenceFailure("inspect", err)
	}
	if conversation, found := owner.sessions.Get(identifier); found {
		return LogSnapshot{Header: conversation.Header(), Events: conversation.Events()}, nil
	}
	if !headersEqual(*listed, loaded.Header) {
		return LogSnapshot{}, sourceConflict(*listed, loaded.Header)
	}
	return LogSnapshot{Header: cloneHeader(loaded.Header), Events: cloneEvents(loaded.Events)}, nil
}

func validateLogical(metadata session.Header, entries []session.Event) error {
	sessionMetadata := session.Metadata{
		CreatedAt: &metadata.CreatedAt, CWD: cloneTextPointer(metadata.CWD),
		ParentSession: cloneIDPointer(metadata.ParentSession), SeedLength: cloneIntPointer(metadata.SeedLength),
		Origin: metadata.Origin, DelegationDepth: cloneIntPointer(metadata.DelegationDepth),
		AgentPreset: cloneTextPointer(metadata.AgentPreset),
	}
	_, err := session.New(metadata.ID, session.CreateOptions{Seed: entries, Metadata: sessionMetadata})
	return err
}

func foldTitle(metadata session.Header, entries []session.Event) (*TitleObservation, error) {
	folded, err := sessiontitle.Fold(entries)
	if err != nil {
		return nil, err
	}
	return &TitleObservation{Header: cloneHeader(metadata), Title: folded}, nil
}

func sessionMatches(candidate SessionRecord, constraints SessionConstraints) bool {
	if len(constraints.IDs) != 0 && !slices.Contains(constraints.IDs, candidate.Header.ID) {
		return false
	}
	if len(constraints.CWDs) != 0 && !nullableTextMatches(constraints.CWDs, candidate.Header.CWD) {
		return false
	}
	if constraints.CreatedAt != nil && !rangeMatches(candidate.Header.CreatedAt, *constraints.CreatedAt) {
		return false
	}
	if len(constraints.Parents) != 0 && !nullableIDMatches(constraints.Parents, candidate.Header.ParentSession) {
		return false
	}
	if len(constraints.Availability) != 0 {
		available := false
		for _, value := range constraints.Availability {
			available = available || value == AvailabilityLive && candidate.Live || value == AvailabilityPersisted && candidate.Persisted
		}
		if !available {
			return false
		}
	}
	return true
}

func eventMatches(entry Document, constraints EventConstraints) bool {
	return (constraints.Sequences == nil || rangeMatches(entry.Seq, *constraints.Sequences)) &&
		(constraints.Times == nil || rangeMatches(entry.Time, *constraints.Times)) &&
		(len(constraints.Types) == 0 || slices.Contains(constraints.Types, entry.Type)) &&
		(len(constraints.Surfaces) == 0 || slices.Contains(constraints.Surfaces, entry.Surface))
}

func nullableTextMatches(values []NullableText, target *string) bool {
	for _, value := range values {
		if optionalTextEqual(value.Value, target) {
			return true
		}
	}
	return false
}

func nullableIDMatches(values []NullableSessionID, target *session.SessionID) bool {
	for _, value := range values {
		if optionalIDEqual(value.Value, target) {
			return true
		}
	}
	return false
}

func rangeMatches(value int64, interval Range) bool {
	return (interval.From == nil || value >= *interval.From) && (interval.To == nil || value <= *interval.To)
}

func cloneRecord(source SessionRecord) SessionRecord {
	return SessionRecord{Header: cloneHeader(source.Header), Live: source.Live, Persisted: source.Persisted}
}

func buildLineageChildren(
	children map[session.SessionID][]SessionRecord,
	identifier session.SessionID,
	path map[session.SessionID]struct{},
) ([]LineageNode, error) {
	result := make([]LineageNode, 0, len(children[identifier]))
	for _, child := range children[identifier] {
		if _, cycle := path[child.Header.ID]; cycle {
			return nil, failure(
				ErrorInvalidLineage,
				fmt.Sprintf("session query: Session lineage contains a descendant cycle at %q", child.Header.ID),
				nil,
			)
		}
		childPath := make(map[session.SessionID]struct{}, len(path)+1)
		for visited := range path {
			childPath[visited] = struct{}{}
		}
		childPath[child.Header.ID] = struct{}{}
		descendants, err := buildLineageChildren(children, child.Header.ID, childPath)
		if err != nil {
			return nil, err
		}
		result = append(result, LineageNode{Session: cloneRecord(child), Descendants: descendants})
	}
	return result, nil
}

type logAnalysis struct {
	records        []EventRecord
	current        []int64
	replacedBy     map[int64]int64
	replacedEvents map[int64][]int64
}

func analyzeLog(metadata session.Header, entries []session.Event) (logAnalysis, error) {
	if err := validateLogical(metadata, entries); err != nil {
		return logAnalysis{}, failure(ErrorInvalidSurface, "session query: invalid Session surface", err)
	}
	nodes := make([]int64, 0)
	replacedBy := make(map[int64]int64)
	replacedEvents := make(map[int64][]int64)
	for _, entry := range entries {
		if entry.SurfaceOp == nil {
			continue
		}
		switch entry.SurfaceOp.Kind {
		case session.SurfaceOperationAppend:
			nodes = append(nodes, entry.Seq)
		case session.SurfaceOperationReplace:
			start := sequencePosition(nodes, entry.SurfaceOp.Start)
			end := sequencePosition(nodes, entry.SurfaceOp.End)
			if start < 0 || end < start {
				return logAnalysis{}, failure(ErrorInvalidSurface, "session query: invalid replacement range", nil)
			}
			removed := append([]int64{}, nodes[start:end+1]...)
			replacedEvents[entry.Seq] = removed
			for _, sequence := range removed {
				replacedBy[sequence] = entry.Seq
			}
			nodes = append(append(append([]int64{}, nodes[:start]...), entry.Seq), nodes[end+1:]...)
		}
	}
	currentSet := make(map[int64]struct{}, len(nodes))
	for _, sequence := range nodes {
		currentSet[sequence] = struct{}{}
	}
	records := make([]EventRecord, len(entries))
	for position, entry := range entries {
		placement := SurfaceLogOnly
		if _, retained := currentSet[entry.Seq]; retained {
			placement = SurfaceCurrent
		} else if _, shadowed := replacedBy[entry.Seq]; shadowed {
			placement = SurfaceShadowed
		}
		records[position] = EventRecord{
			SessionID: metadata.ID, Seq: entry.Seq, Type: entry.Type,
			Time: entry.Time, Surface: placement,
		}
	}
	return logAnalysis{records: records, current: nodes, replacedBy: replacedBy, replacedEvents: replacedEvents}, nil
}

func sourceSequences(entry session.Event) []int64 {
	if entry.SourceEventSeqs == nil {
		return []int64{}
	}
	return append([]int64{}, (*entry.SourceEventSeqs)...)
}

func cloneEvents(source []session.Event) []session.Event {
	result := make([]session.Event, len(source))
	for position, entry := range source {
		result[position] = cloneEvent(entry)
	}
	return result
}

func cloneEvent(source session.Event) session.Event {
	result := source
	result.Data = append([]byte(nil), source.Data...)
	if source.SourceEventSeqs != nil {
		sequences := append([]int64{}, (*source.SourceEventSeqs)...)
		result.SourceEventSeqs = &sequences
	}
	if source.SurfaceOp != nil {
		operation := *source.SurfaceOp
		result.SurfaceOp = &operation
	}
	return result
}

func cloneTextPointer(source *string) *string {
	if source == nil {
		return nil
	}
	value := *source
	return &value
}

func cloneIDPointer(source *session.SessionID) *session.SessionID {
	if source == nil {
		return nil
	}
	value := *source
	return &value
}

func cloneIntPointer(source *int64) *int64 {
	if source == nil {
		return nil
	}
	value := *source
	return &value
}

func sessionNotFound(identifier session.SessionID) error {
	return failure(
		ErrorSessionNotFound,
		fmt.Sprintf("session query: session %q was not found", identifier),
		nil,
	)
}

func eventNotFound(identifier session.SessionID, sequence int64) error {
	return failure(
		ErrorEventNotFound,
		fmt.Sprintf("session query: session %q has no event at seq %d", identifier, sequence),
		nil,
	)
}
