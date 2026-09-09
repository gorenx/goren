package server

import (
	"context"
	"errors"
	"time"

	"github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/workspace"
	workspaceactor "github.com/gorenx/goren/workspace/actor"
	"github.com/gorenx/goren/workspace/persistence/sqlite"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// initializeWorkspaces connects Session headers before opening Workspace storage.
// A successful spawn transfers that storage to the Workspace actor. Composition
// must stop the returned PID before releasing the borrowed Session dependencies.
func initializeWorkspaces(
	ctx context.Context,
	system goakt.ActorSystem,
	sessions *goakt.GrainIdentity,
	history *persistence.Reader,
	settings sqlite.Config,
	timeout time.Duration,
) (*goakt.PID, error) {
	if ctx == nil ||
		system == nil ||
		history == nil {
		return nil, errors.New("server: Workspace context, ActorSystem and Session history are required")
	}
	err := ctx.Err()
	if err != nil {
		return nil, err
	}
	if system.TopicActor() == nil {
		return nil, errors.New("server: Workspace requires PubSub")
	}
	headers, err := workspaceactor.NewHeaders(system, sessions, history, timeout)
	if err != nil {
		return nil, err
	}
	storage, err := sqlite.Open(ctx, settings)
	if err != nil {
		return nil, err
	}
	endpoint, err := workspaceactor.Spawn(ctx, system, "workspace", storage, workspace.RegistryOptions{
		SessionHeads: headers,
	})
	if err != nil {
		return nil, errors.Join(err, storage.Close(context.WithoutCancel(ctx)))
	}
	return endpoint, nil
}
