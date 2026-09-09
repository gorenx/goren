package workspaceactor

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
	sessionregistry "github.com/gorenx/goren/session/registry"
	"github.com/gorenx/goren/workspace"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// History supplies validated durable Session metadata for Workspace recovery.
type History interface {
	Inspect(context.Context, session.SessionID) (persistence.Inspection, error)
	List(context.Context, persistence.SessionPage) (persistence.HeaderPage, error)
}

// Headers merges admitted and durable Session metadata without owning either.
// Its synchronous reads run outside Actor receive turns.
type Headers struct {
	system   goakt.ActorSystem
	registry *goakt.GrainIdentity
	history  History
	timeout  time.Duration
}

// NewHeaders binds existing readers without opening storage or starting actors.
func NewHeaders(
	system goakt.ActorSystem,
	registry *goakt.GrainIdentity,
	durable History,
	timeout time.Duration,
) (*Headers, error) {
	if system == nil ||
		registry == nil ||
		durable == nil ||
		timeout <= 0 {
		return nil, errors.New("workspace: Session Registry, history and positive timeout required")
	}
	return &Headers{
		system:   system,
		registry: registry,
		history:  durable,
		timeout:  timeout,
	}, nil
}

// Get prefers admitted metadata, including empty Sessions not yet persisted.
// A nil header with no error means neither source contains the requested Session.
func (source *Headers) Get(ctx context.Context, identifier session.SessionID) (*session.Header, error) {
	payload, err := source.system.AskGrain(
		ctx,
		source.registry,
		sessionregistry.FindSession{
			SessionID: identifier,
		},
		source.timeout,
	)
	if err != nil {
		return nil, err
	}
	found, ok := payload.(sessionregistry.FoundSession)
	if !ok {
		return nil, errors.New("workspace: invalid Session Header reply")
	}
	if found.Session != nil {
		if found.Session.Header.ID != identifier {
			return nil, errors.New("workspace: Session Header identity mismatch")
		}
		retained := found.Session.Header.Clone()
		return &retained, nil
	}
	inspection, err := source.history.Inspect(ctx, identifier)
	var missing *persistence.NotFoundError
	if errors.As(err, &missing) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	retained := inspection.Header.Clone()
	return &retained, nil
}

// List returns a detached union in Session ID order. This is not an atomic
// cross-owner snapshot; live metadata wins if the same ID appears in both reads.
func (source *Headers) List(ctx context.Context) ([]session.Header, error) {
	byID := make(map[session.SessionID]session.Header)
	page := persistence.SessionPage{
		Limit: 256,
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		batch, err := source.history.List(ctx, page)
		if err != nil {
			return nil, err
		}
		for _, header := range batch.Headers {
			byID[header.ID] = header.Clone()
		}
		if batch.NextCursor == nil {
			break
		}
		page.Cursor = batch.NextCursor
	}
	payload, err := source.system.AskGrain(ctx, source.registry, sessionregistry.ListSessions{}, source.timeout)
	if err != nil {
		return nil, err
	}
	live, ok := payload.(sessionregistry.Sessions)
	if !ok {
		return nil, errors.New("workspace: invalid Session Header listing reply")
	}
	for _, entry := range live.Items {
		byID[entry.Header.ID] = entry.Header.Clone()
	}
	result := make([]session.Header, 0, len(byID))
	for _, header := range byID {
		result = append(result, header)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].ID < result[right].ID
	})
	return result, nil
}

var _ workspace.SessionHeaders = (*Headers)(nil)
