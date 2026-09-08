package workspaceactor

import (
	"context"
	"errors"
	"time"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
	persistenceactor "github.com/gorenx/goren/session/persistence/actor"
	sessionregistry "github.com/gorenx/goren/session/registry"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Headers maps Session Registry and Persistence replies to the Workspace port.
// It runs only during startup or a background Workspace task, never in Receive.
type Headers struct {
	system      goakt.ActorSystem
	registry    *goakt.GrainIdentity
	persistence *goakt.PID
	timeout     time.Duration
}

// NewHeaders binds authoritative live and durable header endpoints.
func NewHeaders(system goakt.ActorSystem, registry *goakt.GrainIdentity, durable *goakt.PID, timeout time.Duration) (*Headers, error) {
	if system == nil || registry == nil || durable == nil || timeout <= 0 {
		return nil, errors.New("workspace: Session header endpoints and positive timeout are required")
	}
	return &Headers{
		system:      system,
		registry:    registry,
		persistence: durable,
		timeout:     timeout,
	}, nil
}

func (source *Headers) live(ctx context.Context) ([]sessionregistry.Session, error) {
	payload, err := source.system.AskGrain(ctx, source.registry, sessionregistry.ListSessions{}, source.timeout)
	if err != nil {
		return nil, err
	}
	result, ok := payload.(sessionregistry.Sessions)
	if !ok {
		return nil, errors.New("workspace: invalid Session Registry reply")
	}
	return result.Items, nil
}

// Get prefers a currently admitted live header, then inspects durable storage.
func (source *Headers) Get(ctx context.Context, id session.SessionID) (session.Header, bool, error) {
	entries, err := source.live(ctx)
	if err != nil {
		return session.Header{}, false, err
	}
	for _, entry := range entries {
		if entry.Header.ID == id {
			return entry.Header.Clone(), true, nil
		}
	}
	payload, err := goakt.Ask(ctx, source.persistence, persistenceactor.Inspect{
		SessionID: id,
	}, source.timeout)
	if err != nil {
		return session.Header{}, false, err
	}
	result, ok := payload.(persistenceactor.Inspection)
	if !ok {
		return session.Header{}, false, errors.New("workspace: invalid durable Session reply")
	}
	var missing *persistence.NotFoundError
	if errors.As(result.Err, &missing) {
		return session.Header{}, false, nil
	}
	if result.Err != nil {
		return session.Header{}, false, result.Err
	}
	return result.Value.Header.Clone(), true, nil
}

// List merges all durable pages with live headers, with live values preferred.
func (source *Headers) List(ctx context.Context) ([]session.Header, error) {
	byID := make(map[session.SessionID]session.Header)
	order := make([]session.SessionID, 0)
	var cursor *persistence.SessionCursor
	for {
		payload, err := goakt.Ask(ctx, source.persistence, persistenceactor.ListSnapshots{
			Page: persistence.SessionPage{
				Cursor: cursor,
				Limit:  256,
			},
		}, source.timeout)
		if err != nil {
			return nil, err
		}
		page, ok := payload.(persistenceactor.Snapshots)
		if !ok {
			return nil, errors.New("workspace: invalid durable header page")
		}
		if page.Err != nil {
			return nil, page.Err
		}
		for _, entry := range page.Value.Snapshots {
			if _, found := byID[entry.Header.ID]; !found {
				order = append(order, entry.Header.ID)
			}
			byID[entry.Header.ID] = entry.Header.Clone()
		}
		if page.Value.NextCursor == nil {
			break
		}
		cursor = page.Value.NextCursor
	}
	entries, err := source.live(ctx)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if _, found := byID[entry.Header.ID]; !found {
			order = append(order, entry.Header.ID)
		}
		byID[entry.Header.ID] = entry.Header.Clone()
	}
	result := make([]session.Header, 0, len(order))
	for _, id := range order {
		result = append(result, byID[id])
	}
	return result, nil
}
