package query

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/gorenx/goren/session"
)

// Config contains provider-independent search pagination policy.
type Config struct {
	DefaultLimit  int
	MaximumLimit  int
	ReadWindowMax *int
}

// Service evaluates one observed Session corpus. Its owner serializes calls.
// Storage lifecycle and source acquisition belong to actor orchestration.
type Service struct {
	sources  map[session.SessionID]Source
	index    Index
	settings Config
	instance string
}

// New validates query policy without opening storage or acquiring sources.
func New(backend Index, settings Config) (*Service, error) {
	if backend == nil {
		return nil, errors.New("session query: Index is required")
	}
	resolved, err := ValidateConfig(settings)
	if err != nil {
		return nil, err
	}
	identity, err := serviceIdentity()
	if err != nil {
		return nil, fmt.Errorf("session query: create service identity: %w", err)
	}
	return &Service{
		sources:  make(map[session.SessionID]Source),
		index:    backend,
		settings: resolved,
		instance: identity,
	}, nil
}

// ValidateConfig resolves and validates query bounds.
func ValidateConfig(settings Config) (Config, error) {
	if settings.DefaultLimit == 0 {
		settings.DefaultLimit = DefaultLimit
	}
	if settings.MaximumLimit == 0 {
		settings.MaximumLimit = DefaultMaximum
	}
	if settings.ReadWindowMax == nil {
		defaultWindow := DefaultReadWindow
		settings.ReadWindowMax = &defaultWindow
	}
	if settings.DefaultLimit < 1 || settings.MaximumLimit < 1 || settings.DefaultLimit > settings.MaximumLimit {
		return Config{}, failure(
			ErrorInvalidConfig,
			"session query: defaultLimit and maximumLimit must be positive and defaultLimit must not exceed maximumLimit",
			nil,
		)
	}
	if *settings.ReadWindowMax < 0 {
		return Config{}, failure(ErrorInvalidConfig, "session query: readWindowMax must be non-negative", nil)
	}
	window := *settings.ReadWindowMax
	settings.ReadWindowMax = &window
	return settings, nil
}

// SearchSessions reconciles the logical corpus and returns one stable ranked
// page grouped by Session.
func (owner *Service) SearchSessions(
	requestContext context.Context,
	criteria SearchSessionsRequest,
) (SessionSearchPage, error) {
	if err := contextFailure(requestContext); err != nil {
		return SessionSearchPage{}, err
	}
	normalized, err := owner.normalizeSessionRequest(criteria)
	if err != nil {
		return SessionSearchPage{}, err
	}
	indexed, err := owner.reconcile(requestContext)
	if err != nil {
		return SessionSearchPage{}, err
	}
	fingerprint, err := requestFingerprint(normalized)
	if err != nil {
		return SessionSearchPage{}, err
	}
	offset, err := owner.cursorOffset(normalized.Cursor, "sessions", fingerprint, indexed.Generation)
	if err != nil {
		return SessionSearchPage{}, err
	}
	hits, err := owner.index.SearchSessions(requestContext, IndexedSearchRequest{
		Text: normalized.Text, Sessions: normalized.Sessions, Events: normalized.Events,
		Offset: offset, Limit: normalized.Limit + 1,
	})
	if err != nil {
		return SessionSearchPage{}, indexFailure(err)
	}
	if err := contextFailure(requestContext); err != nil {
		return SessionSearchPage{}, err
	}
	page := SessionSearchPage{Items: hits}
	if len(page.Items) > normalized.Limit {
		page.Items = page.Items[:normalized.Limit]
		page.NextCursor, err = owner.encodeCursor(cursorEnvelope{
			Version: 1, Instance: owner.instance, Scope: "sessions",
			Fingerprint: fingerprint, Generation: indexed.Generation,
			Offset: offset + normalized.Limit,
		})
		if err != nil {
			return SessionSearchPage{}, err
		}
	}
	if page.Items == nil {
		page.Items = []SessionHit{}
	}
	return page, nil
}

// SearchEvents reconciles the corpus and searches one indexed Session
// generation without making a cold Session live.
func (owner *Service) SearchEvents(
	requestContext context.Context,
	criteria SearchEventsRequest,
) (EventSearchPage, error) {
	if err := contextFailure(requestContext); err != nil {
		return EventSearchPage{}, err
	}
	normalized, err := owner.normalizeEventRequest(criteria)
	if err != nil {
		return EventSearchPage{}, err
	}
	indexed, err := owner.reconcile(requestContext)
	if err != nil {
		return EventSearchPage{}, err
	}
	indexedSource, found := indexed.Sessions[normalized.SessionID]
	if !found {
		return EventSearchPage{}, failure(
			ErrorSessionNotFound,
			fmt.Sprintf("session query: session %q was not found", normalized.SessionID),
			nil,
		)
	}
	fingerprint, err := eventRequestFingerprint(normalized)
	if err != nil {
		return EventSearchPage{}, err
	}
	offset, err := owner.cursorOffset(normalized.Cursor, "events", fingerprint, indexedSource.Generation)
	if err != nil {
		return EventSearchPage{}, err
	}
	metadata, hits, err := owner.index.SearchEvents(requestContext, IndexedEventSearchRequest{
		SessionID: normalized.SessionID, Text: normalized.Text, Events: normalized.Events,
		Offset: offset, Limit: normalized.Limit + 1,
	})
	if err != nil {
		return EventSearchPage{}, indexFailure(err)
	}
	page := EventSearchPage{Session: metadata, Items: hits}
	if len(page.Items) > normalized.Limit {
		page.Items = page.Items[:normalized.Limit]
		page.NextCursor, err = owner.encodeCursor(cursorEnvelope{
			Version: 1, Instance: owner.instance, Scope: "events",
			Fingerprint: fingerprint, Generation: indexedSource.Generation,
			Offset: offset + normalized.Limit,
		})
		if err != nil {
			return EventSearchPage{}, err
		}
	}
	if page.Items == nil {
		page.Items = []EventHit{}
	}
	return page, nil
}

type desiredSource struct {
	header         session.Header
	live           bool
	persisted      bool
	sourceRevision string
	entries        []session.Event
}

func (owner *Service) reconcile(requestContext context.Context) (IndexSnapshot, error) {
	current, err := owner.index.Snapshot(requestContext)
	if err != nil {
		return IndexSnapshot{}, indexFailure(err)
	}
	desired, err := owner.observeCorpus(requestContext)
	if err != nil {
		return IndexSnapshot{}, err
	}
	delta := Reconciliation{}
	identifiers := make([]session.SessionID, 0, len(desired))
	for identifier := range desired {
		identifiers = append(identifiers, identifier)
	}
	sort.Slice(identifiers, func(leftPosition int, rightPosition int) bool {
		return identifiers[leftPosition] < identifiers[rightPosition]
	})
	for _, identifier := range identifiers {
		source := desired[identifier]
		indexed, exists := current.Sessions[identifier]
		replaceDocuments := !exists || indexed.SourceRevision != source.sourceRevision
		metadataChanged := !exists || replaceDocuments || indexed.Live != source.live ||
			indexed.Persisted != source.persisted || !headersEqual(indexed.Header, source.header)
		if !metadataChanged {
			continue
		}
		entries := source.entries
		var documents []Document
		if replaceDocuments {
			documents, err = BuildDocuments(identifier, entries)
			if err != nil {
				return IndexSnapshot{}, err
			}
		}
		delta.Replace = append(delta.Replace, Replacement{
			Session: IndexedSession{
				Header: cloneHeader(source.header), Live: source.live,
				Persisted: source.persisted, SourceRevision: source.sourceRevision,
			},
			ReplaceDocuments: replaceDocuments,
			Documents:        documents,
		})
	}
	for identifier := range current.Sessions {
		if _, retained := desired[identifier]; !retained {
			delta.Remove = append(delta.Remove, identifier)
		}
	}
	sort.Slice(delta.Remove, func(leftPosition int, rightPosition int) bool {
		return delta.Remove[leftPosition] < delta.Remove[rightPosition]
	})
	if len(delta.Replace) == 0 && len(delta.Remove) == 0 {
		return current, nil
	}
	updated, err := owner.index.Reconcile(requestContext, delta)
	if err != nil {
		return IndexSnapshot{}, indexFailure(err)
	}
	return updated, nil
}

func (owner *Service) observeCorpus(requestContext context.Context) (map[session.SessionID]desiredSource, error) {
	if err := contextFailure(requestContext); err != nil {
		return nil, err
	}
	desired := make(map[session.SessionID]desiredSource, len(owner.sources))
	for identifier, source := range owner.sources {
		revision, err := sourceRevision(source.Log)
		if err != nil {
			return nil, err
		}
		desired[identifier] = desiredSource{
			header:         source.Log.Header,
			live:           source.Live,
			persisted:      source.Persisted,
			sourceRevision: revision,
			entries:        source.Log.Events,
		}
	}
	return desired, nil
}

func sourceConflict(left session.Header, right session.Header) error {
	return failure(
		ErrorSourceConflict,
		fmt.Sprintf("session query: source headers conflict for session %q", left.ID),
		nil,
	)
}

func headersEqual(left session.Header, right session.Header) bool {
	return left.Version == right.Version && left.ID == right.ID && left.CreatedAt == right.CreatedAt &&
		optionalTextEqual(left.CWD, right.CWD) && optionalIDEqual(left.ParentSession, right.ParentSession) &&
		optionalIntEqual(left.SeedLength, right.SeedLength) && left.Origin == right.Origin &&
		optionalIntEqual(left.DelegationDepth, right.DelegationDepth) && optionalTextEqual(left.AgentPreset, right.AgentPreset)
}

func optionalTextEqual(left *string, right *string) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}

func optionalIDEqual(left *session.SessionID, right *session.SessionID) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}

func optionalIntEqual(left *int64, right *int64) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}

func cloneHeader(source session.Header) session.Header {
	result := source
	if source.CWD != nil {
		value := *source.CWD
		result.CWD = &value
	}
	if source.ParentSession != nil {
		value := *source.ParentSession
		result.ParentSession = &value
	}
	if source.SeedLength != nil {
		value := *source.SeedLength
		result.SeedLength = &value
	}
	if source.DelegationDepth != nil {
		value := *source.DelegationDepth
		result.DelegationDepth = &value
	}
	if source.AgentPreset != nil {
		value := *source.AgentPreset
		result.AgentPreset = &value
	}
	return result
}

func (owner *Service) normalizeSessionRequest(criteria SearchSessionsRequest) (SearchSessionsRequest, error) {
	textValue, err := normalizeText(criteria.Text)
	if err != nil {
		return SearchSessionsRequest{}, err
	}
	limit, err := owner.normalizeLimit(criteria.Limit)
	if err != nil {
		return SearchSessionsRequest{}, err
	}
	sessions, err := normalizeSessionConstraints(criteria.Sessions)
	if err != nil {
		return SearchSessionsRequest{}, err
	}
	events, err := normalizeEventConstraints(criteria.Events)
	if err != nil {
		return SearchSessionsRequest{}, err
	}
	return SearchSessionsRequest{
		Text: textValue, Sessions: sessions, Events: events,
		Limit: limit, Cursor: criteria.Cursor,
	}, nil
}

func (owner *Service) normalizeEventRequest(criteria SearchEventsRequest) (SearchEventsRequest, error) {
	if criteria.SessionID == "" {
		return SearchEventsRequest{}, failure(ErrorInvalidFilter, "session query: session id is empty", nil)
	}
	textValue, err := normalizeText(criteria.Text)
	if err != nil {
		return SearchEventsRequest{}, err
	}
	limit, err := owner.normalizeLimit(criteria.Limit)
	if err != nil {
		return SearchEventsRequest{}, err
	}
	events, err := normalizeEventConstraints(criteria.Events)
	if err != nil {
		return SearchEventsRequest{}, err
	}
	return SearchEventsRequest{
		SessionID: criteria.SessionID, Text: textValue, Events: events,
		Limit: limit, Cursor: criteria.Cursor,
	}, nil
}

func normalizeText(rawValue string) (string, error) {
	if strings.ContainsRune(rawValue, '\x00') {
		return "", failure(ErrorInvalidQuery, "session query: query must not contain NUL", nil)
	}
	fields := strings.Fields(rawValue)
	if len(fields) == 0 {
		return "", failure(ErrorInvalidQuery, "session query: query must contain non-whitespace text", nil)
	}
	return strings.Join(fields, " "), nil
}

func (owner *Service) normalizeLimit(value int) (int, error) {
	if value == 0 {
		return owner.settings.DefaultLimit, nil
	}
	if value < 1 || value > owner.settings.MaximumLimit {
		return 0, failure(
			ErrorInvalidLimit,
			fmt.Sprintf("session query: limit must be between 1 and %d", owner.settings.MaximumLimit),
			nil,
		)
	}
	return value, nil
}

func normalizeSessionConstraints(source SessionConstraints) (SessionConstraints, error) {
	result := SessionConstraints{
		IDs:          append([]session.SessionID(nil), source.IDs...),
		CWDs:         append([]NullableText(nil), source.CWDs...),
		Parents:      append([]NullableSessionID(nil), source.Parents...),
		Availability: append([]Availability(nil), source.Availability...),
	}
	if source.CreatedAt != nil {
		interval := *source.CreatedAt
		result.CreatedAt = &interval
		if err := validateRange("created-at", interval); err != nil {
			return SessionConstraints{}, err
		}
	}
	for _, value := range result.Availability {
		if value != AvailabilityLive && value != AvailabilityPersisted {
			return SessionConstraints{}, failure(ErrorInvalidFilter, "session query: unknown availability filter", nil)
		}
	}
	return result, nil
}

func normalizeEventConstraints(source EventConstraints) (EventConstraints, error) {
	result := EventConstraints{
		Types:    append([]string(nil), source.Types...),
		Surfaces: append([]Surface(nil), source.Surfaces...),
	}
	if source.Sequences != nil {
		interval := *source.Sequences
		result.Sequences = &interval
		if err := validateRange("seq", interval); err != nil {
			return EventConstraints{}, err
		}
	}
	if source.Times != nil {
		interval := *source.Times
		result.Times = &interval
		if err := validateRange("time", interval); err != nil {
			return EventConstraints{}, err
		}
	}
	for _, value := range result.Surfaces {
		if value != SurfaceCurrent && value != SurfaceShadowed && value != SurfaceLogOnly {
			return EventConstraints{}, failure(ErrorInvalidFilter, "session query: unknown surface filter", nil)
		}
	}
	return result, nil
}

func validateRange(name string, interval Range) error {
	if interval.From != nil && interval.To != nil && *interval.From > *interval.To {
		return failure(ErrorInvalidFilter, "session query: "+name+" range is reversed", nil)
	}
	return nil
}

type cursorEnvelope struct {
	Version     int    `json:"version"`
	Instance    string `json:"instance"`
	Scope       string `json:"scope"`
	Fingerprint string `json:"fingerprint"`
	Generation  int64  `json:"generation"`
	Offset      int    `json:"offset"`
}

func (owner *Service) cursorOffset(value Cursor, scope string, fingerprint string, generation int64) (int, error) {
	if value == "" {
		return 0, nil
	}
	rawValue, err := base64.RawURLEncoding.DecodeString(string(value))
	if err != nil {
		return 0, failure(ErrorInvalidCursor, "session query: cursor is not valid base64url", err)
	}
	var envelope cursorEnvelope
	decoderErr := json.Unmarshal(rawValue, &envelope)
	if decoderErr != nil || envelope.Version != 1 || envelope.Instance != owner.instance ||
		envelope.Scope != scope || envelope.Fingerprint != fingerprint || envelope.Offset < 0 {
		return 0, failure(ErrorInvalidCursor, "session query: cursor does not match this request", decoderErr)
	}
	if envelope.Generation != generation {
		return 0, failure(ErrorStaleCursor, "session query: cursor generation is stale", nil)
	}
	return envelope.Offset, nil
}

func (owner *Service) encodeCursor(envelope cursorEnvelope) (Cursor, error) {
	rawValue, err := json.Marshal(envelope)
	if err != nil {
		return "", failure(ErrorInvalidCursor, "session query: encode cursor", err)
	}
	return Cursor(base64.RawURLEncoding.EncodeToString(rawValue)), nil
}

func requestFingerprint(criteria SearchSessionsRequest) (string, error) {
	copyValue := criteria
	copyValue.Cursor = ""
	copyValue.Sessions.IDs = canonicalIDs(criteria.Sessions.IDs)
	copyValue.Events.Types = canonicalStrings(criteria.Events.Types)
	copyValue.Events.Surfaces = canonicalSurfaces(criteria.Events.Surfaces)
	rawValue, err := json.Marshal(copyValue)
	if err != nil {
		return "", failure(ErrorInvalidFilter, "session query: fingerprint request", err)
	}
	return string(rawValue), nil
}

func eventRequestFingerprint(criteria SearchEventsRequest) (string, error) {
	copyValue := criteria
	copyValue.Cursor = ""
	copyValue.Events.Types = canonicalStrings(criteria.Events.Types)
	copyValue.Events.Surfaces = canonicalSurfaces(criteria.Events.Surfaces)
	rawValue, err := json.Marshal(copyValue)
	if err != nil {
		return "", failure(ErrorInvalidFilter, "session query: fingerprint event request", err)
	}
	return string(rawValue), nil
}

func canonicalIDs(source []session.SessionID) []session.SessionID {
	result := append([]session.SessionID(nil), source...)
	slices.Sort(result)
	return slices.Compact(result)
}

func canonicalStrings(source []string) []string {
	result := append([]string(nil), source...)
	slices.Sort(result)
	return slices.Compact(result)
}

func canonicalSurfaces(source []Surface) []Surface {
	result := append([]Surface(nil), source...)
	slices.Sort(result)
	return slices.Compact(result)
}

func serviceIdentity() (string, error) {
	var randomBytes [16]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(randomBytes[:]), nil
}

func contextFailure(requestContext context.Context) error {
	if requestContext == nil {
		return failure(ErrorAborted, "session query: Context is nil", nil)
	}
	if err := requestContext.Err(); err != nil {
		return failure(ErrorAborted, "session query: operation was aborted", err)
	}
	return nil
}

func indexFailure(cause error) error {
	if cause == nil {
		return nil
	}
	var classified *Error
	if errors.As(cause, &classified) {
		return cause
	}
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return failure(ErrorAborted, "session query: index operation was aborted", cause)
	}
	return failure(ErrorIndexFailed, "session query: derived index failed", cause)
}

// UnicodeCodePoints returns the number of Unicode scalar values in text and
// is shared by adapters that enforce snippet bounds.
func UnicodeCodePoints(textValue string) int {
	return utf8.RuneCountInString(textValue)
}
