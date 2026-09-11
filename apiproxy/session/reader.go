package sessionapi

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
)

// Reader assembles the API read view from independent domain capabilities.
// It owns neither their state nor Actor identities.
type Reader struct {
	logs        Logs
	projections Projections
	agents      Agents
}

// NewReader binds the domains needed for Session list and history views.
func NewReader(history Logs, views Projections, directory Agents) (*Reader, error) {
	if history == nil || views == nil || directory == nil {
		return nil, errors.New("session API: log, projection and Agent readers required")
	}
	return &Reader{
		logs:        history,
		projections: views,
		agents:      directory,
	}, nil
}

// Read returns nil when the Session domain has no admitted history.
func (source *Reader) Read(ctx context.Context, identifier session.SessionID) (*Snapshot, error) {
	history, err := source.logs.Read(ctx, identifier)
	if err != nil || history == nil {
		return nil, err
	}
	result, err := source.assemble(ctx, *history)
	return &result, err
}

// List assembles each returned Session prefix without holding live domain objects.
func (source *Reader) List(ctx context.Context) ([]Snapshot, error) {
	histories, err := source.logs.List(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]Snapshot, 0, len(histories))
	for _, history := range histories {
		view, err := source.assemble(ctx, history)
		if err != nil {
			return nil, err
		}
		result = append(result, view)
	}
	return result, nil
}

func (source *Reader) assemble(ctx context.Context, history Log) (Snapshot, error) {
	projected, err := source.projections.Read(ctx, history.Events)
	if err != nil {
		return Snapshot{}, err
	}
	subject, err := source.agents.Resolve(ctx, history.Header.ID)
	if err != nil {
		return Snapshot{}, err
	}
	running := false
	if subject != nil {
		status, err := subject.Status(ctx)
		if err != nil {
			return Snapshot{}, err
		}
		running = status == agent.StatusRunning
	}
	return Snapshot{
		Header:      history.Header,
		Events:      history.Events,
		Projections: projected,
		Running:     running,
	}, nil
}

var _ LiveSessions = (*Reader)(nil)
