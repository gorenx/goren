package workspace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

// DurableRegistry is the in-process owner of Workspace business state. Every
// mutation is serialized, committed through Backend, then published as a
// post-commit domain event.
type DurableRegistry struct {
	mu                    sync.RWMutex
	sourceScope           *plugin.Scope
	repository            Backend
	headers               SessionHeaders
	clock                 func() time.Time
	newID                 func() (ID, error)
	observerError         func(error)
	entries               map[ID]StoredWorkspace
	workspaceIDs          []ID
	archivedSessionIDs    []session.SessionID
	canonicalSessionPaths map[session.SessionID]string
}

// NewRegistry loads, validates, and when necessary bootstraps the durable
// Workspace registry from existing Session headers.
func NewRegistry(
	requestContext context.Context,
	sourceScope *plugin.Scope,
	repository Backend,
	headers SessionHeaders,
	options RegistryOptions,
) (*DurableRegistry, error) {
	if requestContext == nil || sourceScope == nil || repository == nil || headers == nil {
		return nil, errors.New("workspace: Registry dependencies are incomplete")
	}
	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	newID := options.NewID
	if newID == nil {
		newID = mintID
	}
	reporter := options.ObserverError
	if reporter == nil {
		reporter = func(error) {}
	}
	owner := &DurableRegistry{
		sourceScope: sourceScope, repository: repository, headers: headers,
		clock: clock, newID: newID, observerError: reporter,
		entries: make(map[ID]StoredWorkspace), canonicalSessionPaths: make(map[session.SessionID]string),
	}
	stored, err := repository.Load(requestContext)
	if err != nil {
		return nil, err
	}
	if err := validateDurableState(stored); err != nil {
		return nil, err
	}
	var availableHeaders []session.Header
	if !stored.Initialized || len(stored.Records) != 0 {
		availableHeaders, err = headers.List(requestContext)
		if err != nil {
			return nil, err
		}
		owner.indexHeaders(availableHeaders)
	}
	if !stored.Initialized {
		stored, err = owner.bootstrap(stored, availableHeaders)
		if err != nil {
			return nil, err
		}
		if err := validateDurableState(stored); err != nil {
			return nil, err
		}
		if err := repository.Initialize(requestContext, stored); err != nil {
			return nil, err
		}
	}
	owner.install(stored)
	return owner, nil
}

// Create registers or resolves an existing canonical directory. Repeated
// spellings of the same real directory return one stable Workspace.
func (owner *DurableRegistry) Create(
	requestContext context.Context,
	directory string,
) (Workspace, bool, error) {
	canonical, err := canonicalDirectory(directory)
	if err != nil {
		return nil, false, err
	}
	owner.mu.Lock()
	for identifier, record := range owner.entries {
		if record.Path == canonical {
			owner.mu.Unlock()
			return &entity{owner: owner, identifier: identifier}, false, nil
		}
	}
	identifier, err := owner.newID()
	if err != nil {
		owner.mu.Unlock()
		return nil, false, err
	}
	if identifier == "" {
		owner.mu.Unlock()
		return nil, false, errors.New("workspace: generated Workspace ID is empty")
	}
	if _, exists := owner.entries[identifier]; exists {
		owner.mu.Unlock()
		return nil, false, fmt.Errorf("workspace: generated duplicate ID %q", identifier)
	}
	now := owner.clock().UTC()
	record := StoredWorkspace{
		ID: identifier, Path: canonical, Title: filepath.Base(canonical),
		SessionIDs: []session.SessionID{}, CreatedAt: now, UpdatedAt: now,
	}
	nextOrder := append([]ID{identifier}, owner.workspaceIDs...)
	if err := owner.repository.Create(requestContext, cloneRecord(record), nextOrder); err != nil {
		owner.mu.Unlock()
		return nil, false, err
	}
	owner.entries[identifier] = record
	owner.workspaceIDs = nextOrder
	state := owner.stateLocked(identifier)
	owner.mu.Unlock()
	owner.publishChanged(requestContext, state)
	return &entity{owner: owner, identifier: identifier}, true, nil
}

// Get resolves one registered Workspace by stable identity.
func (owner *DurableRegistry) Get(identifier ID) (Workspace, bool) {
	owner.mu.RLock()
	_, found := owner.entries[identifier]
	owner.mu.RUnlock()
	if !found {
		return nil, false
	}
	return &entity{owner: owner, identifier: identifier}, true
}

// List returns Workspace handles in durable display order.
func (owner *DurableRegistry) List() []Workspace {
	owner.mu.RLock()
	identifiers := append([]ID(nil), owner.workspaceIDs...)
	owner.mu.RUnlock()
	result := make([]Workspace, 0, len(identifiers))
	for _, identifier := range identifiers {
		result = append(result, &entity{owner: owner, identifier: identifier})
	}
	return result
}

// Delete removes only the Workspace registration and retains directories and Session logs.
func (owner *DurableRegistry) Delete(requestContext context.Context, identifier ID) (bool, error) {
	owner.mu.Lock()
	if _, found := owner.entries[identifier]; !found {
		owner.mu.Unlock()
		return false, nil
	}
	nextOrder := slices.DeleteFunc(append([]ID(nil), owner.workspaceIDs...), func(candidate ID) bool {
		return candidate == identifier
	})
	if err := owner.repository.Delete(requestContext, identifier, nextOrder); err != nil {
		owner.mu.Unlock()
		return false, err
	}
	delete(owner.entries, identifier)
	owner.workspaceIDs = nextOrder
	owner.mu.Unlock()
	owner.publishRemoved(requestContext, identifier)
	return true, nil
}

// InsertBefore moves one Workspace in durable display order. A nil anchor appends.
func (owner *DurableRegistry) InsertBefore(
	requestContext context.Context,
	identifier ID,
	beforeIdentifier *ID,
) ([]ID, error) {
	owner.mu.Lock()
	if _, found := owner.entries[identifier]; !found {
		owner.mu.Unlock()
		return nil, &OrderInvalidError{ID: identifier}
	}
	if beforeIdentifier != nil {
		if _, found := owner.entries[*beforeIdentifier]; !found {
			owner.mu.Unlock()
			return nil, &OrderInvalidError{ID: *beforeIdentifier}
		}
		if *beforeIdentifier == identifier {
			result := append([]ID(nil), owner.workspaceIDs...)
			owner.mu.Unlock()
			return result, nil
		}
	}
	without := slices.DeleteFunc(append([]ID(nil), owner.workspaceIDs...), func(candidate ID) bool {
		return candidate == identifier
	})
	insertAt := len(without)
	if beforeIdentifier != nil {
		insertAt = slices.Index(without, *beforeIdentifier)
	}
	nextOrder := slices.Insert(without, insertAt, identifier)
	if slices.Equal(nextOrder, owner.workspaceIDs) {
		result := append([]ID(nil), owner.workspaceIDs...)
		owner.mu.Unlock()
		return result, nil
	}
	if err := owner.repository.SetOrder(requestContext, nextOrder); err != nil {
		owner.mu.Unlock()
		return nil, err
	}
	owner.workspaceIDs = nextOrder
	result := append([]ID(nil), nextOrder...)
	owner.mu.Unlock()
	owner.publishOrder(requestContext, result)
	return result, nil
}

// ArchivedSessionIDs returns a detached registry-global archive set.
func (owner *DurableRegistry) ArchivedSessionIDs() []session.SessionID {
	owner.mu.RLock()
	defer owner.mu.RUnlock()
	return append([]session.SessionID(nil), owner.archivedSessionIDs...)
}

// ArchiveSession adds a known Session to the registry-global archive set.
func (owner *DurableRegistry) ArchiveSession(
	requestContext context.Context,
	identifier session.SessionID,
) error {
	owner.mu.RLock()
	alreadyArchived := slices.Contains(owner.archivedSessionIDs, identifier)
	owner.mu.RUnlock()
	if alreadyArchived {
		return nil
	}
	_, found, err := owner.headers.Get(requestContext, identifier)
	if err != nil {
		return err
	}
	if !found {
		return &UnknownSessionError{SessionID: identifier}
	}
	owner.mu.Lock()
	if slices.Contains(owner.archivedSessionIDs, identifier) {
		owner.mu.Unlock()
		return nil
	}
	nextIDs := append(append([]session.SessionID(nil), owner.archivedSessionIDs...), identifier)
	if err := owner.repository.SetArchivedSessionIDs(requestContext, nextIDs); err != nil {
		owner.mu.Unlock()
		return err
	}
	owner.archivedSessionIDs = nextIDs
	result := append([]session.SessionID(nil), nextIDs...)
	owner.mu.Unlock()
	owner.publishArchived(requestContext, result)
	return nil
}

func (owner *DurableRegistry) readState(identifier ID) (WorkspaceState, bool) {
	owner.mu.RLock()
	defer owner.mu.RUnlock()
	if _, found := owner.entries[identifier]; !found {
		return WorkspaceState{}, false
	}
	return owner.stateLocked(identifier), true
}

func (owner *DurableRegistry) stateLocked(identifier ID) WorkspaceState {
	record := owner.entries[identifier]
	validIDs := make([]session.SessionID, 0, len(record.SessionIDs))
	for _, sessionID := range record.SessionIDs {
		if owner.canonicalSessionPaths[sessionID] == record.Path {
			validIDs = append(validIDs, sessionID)
		}
	}
	return WorkspaceState{
		ID: record.ID, Path: record.Path, Title: record.Title, SessionIDs: validIDs,
		CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}
}

func (owner *DurableRegistry) setTitle(
	requestContext context.Context,
	identifier ID,
	title string,
) error {
	owner.mu.Lock()
	record, found := owner.entries[identifier]
	if !found {
		owner.mu.Unlock()
		return &UnknownError{ID: identifier}
	}
	record = owner.pruneLocked(record)
	if record.Title == title && len(record.SessionIDs) == len(owner.entries[identifier].SessionIDs) {
		owner.mu.Unlock()
		return nil
	}
	record.Title = title
	record.UpdatedAt = owner.clock().UTC()
	if err := owner.repository.Update(requestContext, cloneRecord(record)); err != nil {
		owner.mu.Unlock()
		return err
	}
	owner.entries[identifier] = record
	state := owner.stateLocked(identifier)
	owner.mu.Unlock()
	owner.publishChanged(requestContext, state)
	return nil
}

func (owner *DurableRegistry) attachSession(
	requestContext context.Context,
	identifier ID,
	sessionID session.SessionID,
) error {
	owner.mu.RLock()
	record, found := owner.entries[identifier]
	alreadyAccounted := found && slices.Contains(record.SessionIDs, sessionID)
	owner.mu.RUnlock()
	if !found {
		return &UnknownError{ID: identifier}
	}
	canonical := ""
	if !alreadyAccounted {
		header, headerFound, err := owner.headers.Get(requestContext, sessionID)
		if err != nil {
			return err
		}
		if !headerFound {
			return &AttachError{WorkspaceID: identifier, SessionID: sessionID, Reason: "Session is unknown"}
		}
		if header.CWD == nil {
			return &AttachError{WorkspaceID: identifier, SessionID: sessionID, Reason: "Session header has no cwd"}
		}
		canonical, err = canonicalDirectory(*header.CWD)
		if err != nil {
			return &AttachError{WorkspaceID: identifier, SessionID: sessionID, Reason: err.Error()}
		}
		if canonical != record.Path {
			return &AttachError{
				WorkspaceID: identifier, SessionID: sessionID,
				Reason: fmt.Sprintf("Session cwd resolves to %q, expected %q", canonical, record.Path),
			}
		}
	}
	owner.mu.Lock()
	record, found = owner.entries[identifier]
	if !found {
		owner.mu.Unlock()
		return &UnknownError{ID: identifier}
	}
	if canonical != "" {
		owner.canonicalSessionPaths[sessionID] = canonical
	}
	priorLength := len(record.SessionIDs)
	record = owner.pruneLocked(record)
	if slices.Contains(record.SessionIDs, sessionID) {
		if len(record.SessionIDs) == priorLength {
			owner.mu.Unlock()
			return nil
		}
	} else {
		record.SessionIDs = append([]session.SessionID{sessionID}, record.SessionIDs...)
	}
	record.UpdatedAt = owner.clock().UTC()
	if err := owner.repository.Update(requestContext, cloneRecord(record)); err != nil {
		owner.mu.Unlock()
		return err
	}
	owner.entries[identifier] = record
	state := owner.stateLocked(identifier)
	owner.mu.Unlock()
	owner.publishChanged(requestContext, state)
	return nil
}

func (owner *DurableRegistry) insertSessionBefore(
	requestContext context.Context,
	identifier ID,
	sessionID session.SessionID,
	beforeSessionID *session.SessionID,
) error {
	owner.mu.Lock()
	record, found := owner.entries[identifier]
	if !found {
		owner.mu.Unlock()
		return &UnknownError{ID: identifier}
	}
	record = owner.pruneLocked(record)
	if !slices.Contains(record.SessionIDs, sessionID) ||
		beforeSessionID != nil && !slices.Contains(record.SessionIDs, *beforeSessionID) {
		owner.mu.Unlock()
		return &MoveInvalidError{
			WorkspaceID: identifier, SessionID: sessionID,
			BeforeSessionID: cloneSessionID(beforeSessionID),
		}
	}
	if beforeSessionID != nil && *beforeSessionID == sessionID {
		owner.mu.Unlock()
		return nil
	}
	without := slices.DeleteFunc(append([]session.SessionID(nil), record.SessionIDs...),
		func(candidate session.SessionID) bool { return candidate == sessionID })
	insertAt := len(without)
	if beforeSessionID != nil {
		insertAt = slices.Index(without, *beforeSessionID)
	}
	nextIDs := slices.Insert(without, insertAt, sessionID)
	if slices.Equal(nextIDs, owner.entries[identifier].SessionIDs) {
		owner.mu.Unlock()
		return nil
	}
	record.SessionIDs = nextIDs
	record.UpdatedAt = owner.clock().UTC()
	if err := owner.repository.Update(requestContext, cloneRecord(record)); err != nil {
		owner.mu.Unlock()
		return err
	}
	owner.entries[identifier] = record
	state := owner.stateLocked(identifier)
	owner.mu.Unlock()
	owner.publishChanged(requestContext, state)
	return nil
}

func (owner *DurableRegistry) detachSession(
	requestContext context.Context,
	identifier ID,
	sessionID session.SessionID,
) error {
	owner.mu.Lock()
	record, found := owner.entries[identifier]
	if !found {
		owner.mu.Unlock()
		return &UnknownError{ID: identifier}
	}
	prior := owner.entries[identifier]
	record = owner.pruneLocked(record)
	record.SessionIDs = slices.DeleteFunc(record.SessionIDs, func(candidate session.SessionID) bool {
		return candidate == sessionID
	})
	if slices.Equal(record.SessionIDs, prior.SessionIDs) {
		owner.mu.Unlock()
		return nil
	}
	record.UpdatedAt = owner.clock().UTC()
	if err := owner.repository.Update(requestContext, cloneRecord(record)); err != nil {
		owner.mu.Unlock()
		return err
	}
	owner.entries[identifier] = record
	state := owner.stateLocked(identifier)
	owner.mu.Unlock()
	owner.publishChanged(requestContext, state)
	return nil
}

func (owner *DurableRegistry) pruneLocked(record StoredWorkspace) StoredWorkspace {
	filtered := make([]session.SessionID, 0, len(record.SessionIDs))
	for _, identifier := range record.SessionIDs {
		if owner.canonicalSessionPaths[identifier] == record.Path {
			filtered = append(filtered, identifier)
		}
	}
	record.SessionIDs = filtered
	return record
}

func (owner *DurableRegistry) install(stored StoredRegistry) {
	owner.workspaceIDs = append([]ID(nil), stored.WorkspaceIDs...)
	owner.archivedSessionIDs = append([]session.SessionID(nil), stored.ArchivedSessionIDs...)
	for _, record := range stored.Records {
		owner.entries[record.ID] = cloneRecord(record)
	}
}

func (owner *DurableRegistry) indexHeaders(headers []session.Header) {
	for _, header := range headers {
		if header.CWD == nil {
			continue
		}
		canonical, err := canonicalDirectory(*header.CWD)
		if err == nil {
			owner.canonicalSessionPaths[header.ID] = canonical
		}
	}
}

type bootstrapGroup struct {
	path     string
	headers  []session.Header
	newestAt int64
}

func (owner *DurableRegistry) bootstrap(
	stored StoredRegistry,
	headers []session.Header,
) (StoredRegistry, error) {
	recordsByPath := make(map[string]StoredWorkspace, len(stored.Records))
	for _, record := range stored.Records {
		recordsByPath[record.Path] = cloneRecord(record)
	}
	groupsByPath := make(map[string][]session.Header)
	for _, header := range headers {
		canonical := owner.canonicalSessionPaths[header.ID]
		if canonical != "" {
			groupsByPath[canonical] = append(groupsByPath[canonical], header)
		}
	}
	groups := make([]bootstrapGroup, 0, len(groupsByPath))
	for canonical, groupedHeaders := range groupsByPath {
		sort.Slice(groupedHeaders, func(leftIndex int, rightIndex int) bool {
			if groupedHeaders[leftIndex].CreatedAt != groupedHeaders[rightIndex].CreatedAt {
				return groupedHeaders[leftIndex].CreatedAt > groupedHeaders[rightIndex].CreatedAt
			}
			return groupedHeaders[leftIndex].ID < groupedHeaders[rightIndex].ID
		})
		groups = append(groups, bootstrapGroup{
			path: canonical, headers: groupedHeaders, newestAt: groupedHeaders[0].CreatedAt,
		})
	}
	sort.Slice(groups, func(leftIndex int, rightIndex int) bool {
		if groups[leftIndex].newestAt != groups[rightIndex].newestAt {
			return groups[leftIndex].newestAt > groups[rightIndex].newestAt
		}
		return groups[leftIndex].path < groups[rightIndex].path
	})
	accounted := make(map[session.SessionID]ID)
	for _, record := range stored.Records {
		for _, identifier := range record.SessionIDs {
			accounted[identifier] = record.ID
		}
	}
	for _, group := range groups {
		record, exists := recordsByPath[group.path]
		if !exists {
			sessionIDs := make([]session.SessionID, 0, len(group.headers))
			for _, header := range group.headers {
				if _, found := accounted[header.ID]; !found {
					sessionIDs = append(sessionIDs, header.ID)
				}
			}
			if len(sessionIDs) == 0 {
				continue
			}
			identifier, err := owner.newID()
			if err != nil {
				return StoredRegistry{}, err
			}
			created := time.UnixMilli(group.newestAt).UTC()
			record = StoredWorkspace{
				ID: identifier, Path: group.path, Title: filepath.Base(group.path),
				SessionIDs: sessionIDs, CreatedAt: created, UpdatedAt: created,
			}
			recordsByPath[group.path] = record
			for _, sessionID := range sessionIDs {
				accounted[sessionID] = identifier
			}
			continue
		}

		historical := make([]session.SessionID, 0, len(group.headers))
		for _, header := range group.headers {
			holder, found := accounted[header.ID]
			if !found || holder == record.ID {
				historical = append(historical, header.ID)
			}
		}
		historicalSet := make(map[session.SessionID]struct{}, len(historical))
		for _, sessionID := range historical {
			historicalSet[sessionID] = struct{}{}
		}
		sessionIDs := append([]session.SessionID(nil), historical...)
		for _, sessionID := range record.SessionIDs {
			if _, found := historicalSet[sessionID]; !found {
				sessionIDs = append(sessionIDs, sessionID)
			}
		}
		if !slices.Equal(record.SessionIDs, sessionIDs) {
			record.SessionIDs = sessionIDs
			record.UpdatedAt = owner.clock().UTC()
		}
		for _, sessionID := range historical {
			accounted[sessionID] = record.ID
		}
		recordsByPath[group.path] = record
	}
	records := make([]StoredWorkspace, 0, len(recordsByPath))
	for _, record := range recordsByPath {
		records = append(records, record)
	}
	groupRank := make(map[string]int64, len(groups))
	for _, group := range groups {
		groupRank[group.path] = group.newestAt
	}
	priorRank := make(map[ID]int, len(stored.WorkspaceIDs))
	for index, identifier := range stored.WorkspaceIDs {
		priorRank[identifier] = index
	}
	sort.Slice(records, func(leftIndex int, rightIndex int) bool {
		leftRecord := records[leftIndex]
		rightRecord := records[rightIndex]
		leftTime, leftRanked := groupRank[leftRecord.Path]
		if !leftRanked {
			leftTime = leftRecord.CreatedAt.UnixMilli()
		}
		rightTime, rightRanked := groupRank[rightRecord.Path]
		if !rightRanked {
			rightTime = rightRecord.CreatedAt.UnixMilli()
		}
		if leftTime != rightTime {
			return leftTime > rightTime
		}
		leftPrior, leftKnown := priorRank[leftRecord.ID]
		rightPrior, rightKnown := priorRank[rightRecord.ID]
		if leftKnown != rightKnown {
			return leftKnown
		}
		if leftKnown && leftPrior != rightPrior {
			return leftPrior < rightPrior
		}
		return leftRecord.ID < rightRecord.ID
	})
	workspaceIDs := make([]ID, 0, len(records))
	for _, record := range records {
		workspaceIDs = append(workspaceIDs, record.ID)
	}
	return StoredRegistry{
		Initialized: true, WorkspaceIDs: workspaceIDs,
		ArchivedSessionIDs: append([]session.SessionID(nil), stored.ArchivedSessionIDs...),
		Records:            records,
	}, nil
}

func validateDurableState(stored StoredRegistry) error {
	recordsByID := make(map[ID]StoredWorkspace, len(stored.Records))
	paths := make(map[string]ID, len(stored.Records))
	accounted := make(map[session.SessionID]ID)
	for _, record := range stored.Records {
		if record.ID == "" || record.Path == "" || record.SessionIDs == nil ||
			record.CreatedAt.IsZero() || record.UpdatedAt.IsZero() {
			return errors.New("workspace: persistence returned an incomplete record")
		}
		if _, duplicate := recordsByID[record.ID]; duplicate {
			return fmt.Errorf("workspace: persistence repeats ID %q", record.ID)
		}
		if holder, duplicate := paths[record.Path]; duplicate {
			return fmt.Errorf("workspace: path %q belongs to both %q and %q", record.Path, holder, record.ID)
		}
		for _, identifier := range record.SessionIDs {
			if holder, duplicate := accounted[identifier]; duplicate {
				return fmt.Errorf("workspace: session %q belongs to both %q and %q", identifier, holder, record.ID)
			}
			accounted[identifier] = record.ID
		}
		recordsByID[record.ID] = record
		paths[record.Path] = record.ID
	}
	seen := make(map[ID]struct{}, len(stored.WorkspaceIDs))
	for _, identifier := range stored.WorkspaceIDs {
		if _, duplicate := seen[identifier]; duplicate {
			return fmt.Errorf("workspace: durable order repeats ID %q", identifier)
		}
		if _, found := recordsByID[identifier]; !found {
			return fmt.Errorf("workspace: durable order references missing ID %q", identifier)
		}
		seen[identifier] = struct{}{}
	}
	if stored.Initialized && len(seen) != len(recordsByID) {
		return errors.New("workspace: durable order omits a stored Workspace")
	}
	return nil
}

func canonicalDirectory(directory string) (string, error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("path %q is not a directory", canonical)
	}
	return filepath.Clean(canonical), nil
}

func mintID() (ID, error) {
	var randomBytes [16]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		return "", err
	}
	randomBytes[6] = randomBytes[6]&0x0f | 0x40
	randomBytes[8] = randomBytes[8]&0x3f | 0x80
	encoded := make([]byte, 36)
	hex.Encode(encoded[0:8], randomBytes[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], randomBytes[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], randomBytes[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], randomBytes[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], randomBytes[10:16])
	return ID(string(encoded)), nil
}

func cloneRecord(source StoredWorkspace) StoredWorkspace {
	result := source
	result.SessionIDs = append([]session.SessionID(nil), source.SessionIDs...)
	return result
}

func cloneState(source WorkspaceState) WorkspaceState {
	result := source
	result.SessionIDs = append([]session.SessionID(nil), source.SessionIDs...)
	return result
}

func cloneSessionID(source *session.SessionID) *session.SessionID {
	if source == nil {
		return nil
	}
	result := *source
	return &result
}

func (owner *DurableRegistry) publishChanged(requestContext context.Context, state WorkspaceState) {
	owner.publish("changed", func() error {
		return plugin.EmitFrom(requestContext, owner.sourceScope, changedTopic, ChangedNotice{WorkspaceState: cloneState(state)})
	})
}

func (owner *DurableRegistry) publishRemoved(requestContext context.Context, identifier ID) {
	owner.publish("removed", func() error {
		return plugin.EmitFrom(requestContext, owner.sourceScope, removedTopic, RemovedNotice{ID: identifier})
	})
}

func (owner *DurableRegistry) publishOrder(requestContext context.Context, identifiers []ID) {
	owner.publish("order-changed", func() error {
		return plugin.EmitFrom(requestContext, owner.sourceScope, orderTopic,
			OrderChangedNotice{WorkspaceIDs: append([]ID(nil), identifiers...)})
	})
}

func (owner *DurableRegistry) publishArchived(requestContext context.Context, identifiers []session.SessionID) {
	owner.publish("archived-sessions-changed", func() error {
		return plugin.EmitFrom(requestContext, owner.sourceScope, archiveTopic,
			ArchivedSessionsChangedNotice{SessionIDs: append([]session.SessionID(nil), identifiers...)})
	})
}

func (owner *DurableRegistry) publish(name string, operation func() error) {
	if err := operation(); err != nil {
		owner.observerError(fmt.Errorf("workspace: %s observer: %w", name, err))
	}
}
