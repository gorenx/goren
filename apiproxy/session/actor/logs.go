package sessionadapter

import (
	"context"
	"errors"
	"time"

	sessionapi "github.com/gorenx/goren/apiproxy/session"
	eventadapter "github.com/gorenx/goren/apiproxy/session/events/actor"
	"github.com/gorenx/goren/session"
	sessionregistry "github.com/gorenx/goren/session/registry"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Logs implements Session history reads independently of Agent and projection state.
type Logs struct {
	system    goakt.ActorSystem
	registry  *goakt.GrainIdentity
	snapshots *eventadapter.Snapshots
	timeout   time.Duration
}

// NewLogs binds the existing Session lifecycle endpoint.
func NewLogs(system goakt.ActorSystem, registry *goakt.GrainIdentity, timeout time.Duration) (*Logs, error) {
	source, err := eventadapter.NewSnapshots(system, registry, timeout)
	if err != nil {
		return nil, err
	}
	return &Logs{
		system:    system,
		registry:  registry,
		snapshots: source,
		timeout:   timeout,
	}, nil
}

// Read captures the selected creation before reading its history.
func (source *Logs) Read(ctx context.Context, identifier session.SessionID) (*sessionapi.Log, error) {
	payload, err := source.system.AskGrain(ctx, source.registry, sessionregistry.FindSession{
		SessionID: identifier,
	}, source.timeout)
	if err != nil {
		return nil, err
	}
	selected, ok := payload.(sessionregistry.FoundSession)
	if !ok {
		return nil, errors.New("session API: invalid Session lookup reply")
	}
	if selected.Session == nil {
		return nil, nil
	}
	if selected.Session.Header.ID != identifier {
		return nil, errors.New("session API: Session lookup identity mismatch")
	}
	history, err := source.snapshots.ReadSnapshot(ctx, identifier, selected.Session.CreationID)
	if err != nil {
		return nil, err
	}
	return &sessionapi.Log{
		Header: history.Header,
		Events: history.Events,
	}, nil
}

// List reads admitted histories; closed creations are handled by the Session adapter.
func (source *Logs) List(ctx context.Context) ([]sessionapi.Log, error) {
	histories, err := source.snapshots.ListSnapshots(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]sessionapi.Log, 0, len(histories))
	for _, history := range histories {
		result = append(result, sessionapi.Log{
			Header: history.Header,
			Events: history.Events,
		})
	}
	return result, nil
}

var _ sessionapi.Logs = (*Logs)(nil)
