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
	"time"

	"github.com/gorenx/goren/session"
)

// DurableRegistry is the in-process owner of Workspace business state. Every
// mutation is committed through Backend before recording a post-commit notice.
// Its owner must serialize access; this object has no framework or lock.
type DurableRegistry struct {
	notices               []Notice
	repository            Backend
	headers               SessionHeaders
	timeSource            TimeSource
	identifierGenerator   IDGenerator
	entries               map[ID]StoredWorkspace
	workspaceIDs          []ID
	archivedSessionIDs    []session.SessionID
	canonicalSessionPaths map[session.SessionID]string
}

type systemTimeSource struct{}

func (systemTimeSource) CurrentTime() time.Time {
	return time.Now()
}

type randomIDGenerator struct{}

func (randomIDGenerator) NewWorkspaceID() (ID, error) {
	return mintID()
}

// NewRegistry loads the registry from an already opened Backend. The caller
// retains ownership of Backend if initialization fails.
func NewRegistry(
	requestContext context.Context,
	repository Backend,
	options RegistryOptions,
) (*DurableRegistry, error) {
	if repository == nil || options.SessionHeads == nil {
		return nil, errors.New("workspace: Backend and SessionHeaders are required")
	}
	selectedTimeSource := options.TimeSource
	if selectedTimeSource == nil {
		selectedTimeSource = systemTimeSource{}
	}
	identifierGenerator := options.IDGenerator
	if identifierGenerator == nil {
		identifierGenerator = randomIDGenerator{}
	}
	owner := &DurableRegistry{
		repository:            repository,
		headers:               options.SessionHeads,
		timeSource:            selectedTimeSource,
		identifierGenerator:   identifierGenerator,
		entries:               make(map[ID]StoredWorkspace),
		canonicalSessionPaths: make(map[session.SessionID]string),
	}
	if err := owner.initialize(requestContext); err != nil {
		return nil, err
	}
	return owner, nil
}

// Close releases storage after the owner has drained accepted operations.
func (owner *DurableRegistry) Close(ctx context.Context) error {
	return owner.repository.Close(ctx)
}

func (owner *DurableRegistry) initialize(requestContext context.Context) error {
	repository := owner.repository
	headers := owner.headers
	stored, err := repository.Load(requestContext)
	if err != nil {
		return err
	}
	if err := validateDurableState(stored); err != nil {
		return err
	}
	var availableHeaders []session.Header
	if !stored.Initialized || len(stored.Records) != 0 {
		availableHeaders, err = headers.List(requestContext)
		if err != nil {
			return err
		}
		owner.indexHeaders(availableHeaders)
	}
	if !stored.Initialized {
		stored, err = owner.bootstrap(stored, availableHeaders)
		if err != nil {
			return err
		}
		if err := validateDurableState(stored); err != nil {
			return err
		}
		if err := repository.Initialize(requestContext, stored); err != nil {
			return err
		}
	}
	owner.install(stored)
	return nil
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

	for identifier, record := range owner.entries {
		if record.Path == canonical {
			return &entity{
				owner:      owner,
				identifier: identifier,
			}, false, nil
		}
	}
	identifier, err := owner.identifierGenerator.NewWorkspaceID()
	if err != nil {
		return nil, false, err
	}
	if identifier == "" {
		return nil, false, errors.New("workspace: generated Workspace ID is empty")
	}
	if _, exists := owner.entries[identifier]; exists {
		return nil, false, fmt.Errorf("workspace: generated duplicate ID %q", identifier)
	}
	now := owner.timeSource.CurrentTime().UTC()
	record := StoredWorkspace{
		ID:         identifier,
		Path:       canonical,
		Title:      filepath.Base(canonical),
		SessionIDs: []session.SessionID{},
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	nextOrder := append([]ID{identifier}, owner.workspaceIDs...)
	if err := owner.repository.Create(requestContext, cloneRecord(record), nextOrder); err != nil {
		return nil, false, err
	}
	owner.entries[identifier] = record
	owner.workspaceIDs = nextOrder
	state := owner.state(identifier)

	owner.recordChanged(state)
	return &entity{
		owner:      owner,
		identifier: identifier,
	}, true, nil
}

// Get resolves one registered Workspace by stable identity.
func (owner *DurableRegistry) Get(identifier ID) (Workspace, bool) {
	_, found := owner.entries[identifier]

	if !found {
		return nil, false
	}
	return &entity{
		owner:      owner,
		identifier: identifier,
	}, true
}

// List returns Workspace handles in durable display order.
func (owner *DurableRegistry) List() []Workspace {
	identifiers := append([]ID(nil), owner.workspaceIDs...)

	result := make([]Workspace, 0, len(identifiers))
	for _, identifier := range identifiers {
		result = append(result, &entity{
			owner:      owner,
			identifier: identifier,
		})
	}
	return result
}

// Delete removes only the Workspace registration and retains directories and Session logs.
func (owner *DurableRegistry) Delete(requestContext context.Context, identifier ID) (bool, error) {
	if _, found := owner.entries[identifier]; !found {
		return false, nil
	}
	nextOrder := slices.DeleteFunc(append([]ID(nil), owner.workspaceIDs...), func(candidate ID) bool {
		return candidate == identifier
	})
	if err := owner.repository.Delete(requestContext, identifier, nextOrder); err != nil {
		return false, err
	}
	delete(owner.entries, identifier)
	owner.workspaceIDs = nextOrder

	owner.recordRemoved(identifier)
	return true, nil
}

// InsertBefore moves one Workspace in durable display order. A nil anchor appends.
func (owner *DurableRegistry) InsertBefore(
	requestContext context.Context,
	identifier ID,
	beforeIdentifier *ID,
) ([]ID, error) {
	if _, found := owner.entries[identifier]; !found {
		return nil, &OrderInvalidError{
			ID: identifier,
		}
	}
	if beforeIdentifier != nil {
		if _, found := owner.entries[*beforeIdentifier]; !found {
			return nil, &OrderInvalidError{
				ID: *beforeIdentifier,
			}
		}
		if *beforeIdentifier == identifier {
			result := append([]ID(nil), owner.workspaceIDs...)
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
		return result, nil
	}
	if err := owner.repository.SetOrder(requestContext, nextOrder); err != nil {
		return nil, err
	}
	owner.workspaceIDs = nextOrder
	result := append([]ID(nil), nextOrder...)

	owner.recordOrder(result)
	return result, nil
}

// ArchivedSessionIDs returns a detached registry-global archive set.
func (owner *DurableRegistry) ArchivedSessionIDs() []session.SessionID {
	return append([]session.SessionID(nil), owner.archivedSessionIDs...)
}

// ArchiveSession adds a known Session to the registry-global archive set.
func (owner *DurableRegistry) ArchiveSession(
	requestContext context.Context,
	identifier session.SessionID,
) error {
	alreadyArchived := slices.Contains(owner.archivedSessionIDs, identifier)

	if alreadyArchived {
		return nil
	}
	_, found, err := owner.headers.Get(requestContext, identifier)
	if err != nil {
		return err
	}
	if !found {
		return &UnknownSessionError{
			SessionID: identifier,
		}
	}

	nextIDs := append(append([]session.SessionID(nil), owner.archivedSessionIDs...), identifier)
	if err := owner.repository.SetArchivedSessionIDs(requestContext, nextIDs); err != nil {
		return err
	}
	owner.archivedSessionIDs = nextIDs
	result := append([]session.SessionID(nil), nextIDs...)

	owner.recordArchived(result)
	return nil
}

func (owner *DurableRegistry) readState(identifier ID) (WorkspaceState, bool) {
	if _, found := owner.entries[identifier]; !found {
		return WorkspaceState{}, false
	}
	return owner.state(identifier), true
}

func (owner *DurableRegistry) state(identifier ID) WorkspaceState {
	record := owner.entries[identifier]
	validIDs := make([]session.SessionID, 0, len(record.SessionIDs))
	for _, sessionID := range record.SessionIDs {
		if owner.canonicalSessionPaths[sessionID] == record.Path {
			validIDs = append(validIDs, sessionID)
		}
	}
	return WorkspaceState{
		ID:         record.ID,
		Path:       record.Path,
		Title:      record.Title,
		SessionIDs: validIDs,
		CreatedAt:  record.CreatedAt,
		UpdatedAt:  record.UpdatedAt,
	}
}

// SetTitle updates the title of an existing workspace.
func (owner *DurableRegistry) SetTitle(
	requestContext context.Context,
	identifier ID,
	title string,
) error {
	record, found := owner.entries[identifier]
	if !found {
		return &UnknownError{
			ID: identifier,
		}
	}
	record.Title = title
	return owner.update(requestContext, record)
}

// AttachSession validates and attaches a Session to an existing workspace.
func (owner *DurableRegistry) AttachSession(
	requestContext context.Context,
	identifier ID,
	sessionID session.SessionID,
) error {
	record, found := owner.entries[identifier]
	alreadyAccounted := found && slices.Contains(record.SessionIDs, sessionID)

	if !found {
		return &UnknownError{
			ID: identifier,
		}
	}
	canonical := ""
	if !alreadyAccounted {
		header, headerFound, err := owner.headers.Get(requestContext, sessionID)
		if err != nil {
			return err
		}
		if !headerFound {
			return &AttachError{
				WorkspaceID: identifier,
				SessionID:   sessionID,
				Reason:      "Session is unknown",
			}
		}
		if header.CWD == nil {
			return &AttachError{
				WorkspaceID: identifier,
				SessionID:   sessionID,
				Reason:      "Session header has no cwd",
			}
		}
		canonical, err = canonicalDirectory(*header.CWD)
		if err != nil {
			return &AttachError{
				WorkspaceID: identifier,
				SessionID:   sessionID,
				Reason:      err.Error(),
			}
		}
		if canonical != record.Path {
			return &AttachError{
				WorkspaceID: identifier,
				SessionID:   sessionID,
				Reason:      fmt.Sprintf("Session cwd resolves to %q, expected %q", canonical, record.Path),
			}
		}
	}

	if canonical != "" {
		owner.canonicalSessionPaths[sessionID] = canonical
	}
	if !alreadyAccounted {
		record.SessionIDs = append([]session.SessionID{sessionID}, record.SessionIDs...)
	}
	return owner.update(requestContext, record)
}

// InsertSessionBefore reorders an existing Session membership.
func (owner *DurableRegistry) InsertSessionBefore(
	requestContext context.Context,
	identifier ID,
	sessionID session.SessionID,
	beforeSessionID *session.SessionID,
) error {
	record, found := owner.entries[identifier]
	if !found {
		return &UnknownError{
			ID: identifier,
		}
	}
	if !slices.Contains(record.SessionIDs, sessionID) ||
		beforeSessionID != nil && !slices.Contains(record.SessionIDs, *beforeSessionID) {
		return &MoveInvalidError{
			WorkspaceID:     identifier,
			SessionID:       sessionID,
			BeforeSessionID: cloneSessionID(beforeSessionID),
		}
	}
	if beforeSessionID != nil && *beforeSessionID == sessionID {
		return owner.update(requestContext, record)
	}
	without := slices.DeleteFunc(append([]session.SessionID(nil), record.SessionIDs...),
		func(candidate session.SessionID) bool { return candidate == sessionID })
	insertAt := len(without)
	if beforeSessionID != nil {
		insertAt = slices.Index(without, *beforeSessionID)
	}
	nextIDs := slices.Insert(without, insertAt, sessionID)
	record.SessionIDs = nextIDs
	return owner.update(requestContext, record)
}

// DetachSession removes a Session membership without deleting its log.
func (owner *DurableRegistry) DetachSession(
	requestContext context.Context,
	identifier ID,
	sessionID session.SessionID,
) error {
	record, found := owner.entries[identifier]
	if !found {
		return &UnknownError{
			ID: identifier,
		}
	}
	record.SessionIDs = slices.DeleteFunc(slices.Clone(record.SessionIDs), func(candidate session.SessionID) bool {
		return candidate == sessionID
	})
	return owner.update(requestContext, record)
}

// update applies membership pruning to every accepted mutation, including a
// no-op move. Failed validation never reaches this commit boundary.
func (owner *DurableRegistry) update(ctx context.Context, record StoredWorkspace) error {
	previous := owner.entries[record.ID]
	record = owner.prune(record)
	if previous.Title == record.Title && slices.Equal(previous.SessionIDs, record.SessionIDs) {
		return nil
	}
	record.UpdatedAt = owner.timeSource.CurrentTime().UTC()
	if err := owner.repository.Update(ctx, cloneRecord(record)); err != nil {
		return err
	}
	owner.entries[record.ID] = record
	owner.recordChanged(owner.state(record.ID))
	return nil
}

func (owner *DurableRegistry) prune(record StoredWorkspace) StoredWorkspace {
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
			path:     canonical,
			headers:  groupedHeaders,
			newestAt: groupedHeaders[0].CreatedAt,
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
			identifier, err := owner.identifierGenerator.NewWorkspaceID()
			if err != nil {
				return StoredRegistry{}, err
			}
			created := time.UnixMilli(group.newestAt).UTC()
			record = StoredWorkspace{
				ID:         identifier,
				Path:       group.path,
				Title:      filepath.Base(group.path),
				SessionIDs: sessionIDs,
				CreatedAt:  created,
				UpdatedAt:  created,
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
			record.UpdatedAt = owner.timeSource.CurrentTime().UTC()
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
		Initialized:        true,
		WorkspaceIDs:       workspaceIDs,
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
	if !filepath.IsAbs(directory) {
		return "", fmt.Errorf("workspace: path must be fully qualified: %q", directory)
	}
	canonical, err := filepath.EvalSymlinks(directory)
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

// TakeNotices transfers committed notifications to the delivery adapter.
func (owner *DurableRegistry) TakeNotices() []Notice {
	notices := owner.notices
	owner.notices = nil
	return notices
}

func (owner *DurableRegistry) recordChanged(state WorkspaceState) {
	owner.notices = append(owner.notices, ChangedNotice{
		WorkspaceState: cloneState(state),
	})
}
func (owner *DurableRegistry) recordRemoved(identifier ID) {
	owner.notices = append(owner.notices, RemovedNotice{
		ID: identifier,
	})
}
func (owner *DurableRegistry) recordOrder(identifiers []ID) {
	owner.notices = append(owner.notices, OrderChangedNotice{
		WorkspaceIDs: append([]ID(nil), identifiers...),
	})
}
func (owner *DurableRegistry) recordArchived(identifiers []session.SessionID) {
	owner.notices = append(owner.notices, ArchivedSessionsChangedNotice{
		SessionIDs: append([]session.SessionID(nil), identifiers...),
	})
}
