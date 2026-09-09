// Package approvalactor implements Session messaging for the approval tool.
// It defines no approval Actor; the common ToolActor owns execution scheduling.
package approvalactor

import (
	"context"
	"errors"
	"time"

	approvaltool "github.com/gorenx/goren/approval/tool"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	sessionregistry "github.com/gorenx/goren/session/registry"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Sessions locates live logs for task-side approval execution.
type Sessions struct {
	system    goakt.ActorSystem
	directory *goakt.GrainIdentity
	timeout   time.Duration
}

// NewSessions binds the shared Session Registry. Methods run in tool tasks,
// never inside a Grain dispatcher callback.
func NewSessions(system goakt.ActorSystem, directory *goakt.GrainIdentity, timeout time.Duration) (*Sessions, error) {
	if system == nil || directory == nil || timeout <= 0 {
		return nil, errors.New("approval: Session endpoint and timeout are required")
	}
	return &Sessions{
		system:    system,
		directory: directory,
		timeout:   timeout,
	}, nil
}

// Open fixes the creation identity before reading or committing its audit log.
func (source *Sessions) Open(ctx context.Context, id session.SessionID) (approvaltool.Log, error) {
	payload, err := source.system.AskGrain(ctx, source.directory, sessionregistry.FindSession{
		SessionID: id,
	}, source.timeout)
	if err != nil {
		return nil, err
	}
	found, valid := payload.(sessionregistry.FoundSession)
	if !valid || found.Session == nil || found.Session.Header.ID != id {
		return nil, errors.New("approval: live Session was not found")
	}
	creation := found.Session.CreationID
	payload, err = source.system.AskGrain(ctx, source.directory, sessionregistry.ResolveSession{
		SessionID:  id,
		CreationID: creation,
	}, source.timeout)
	if err != nil {
		return nil, err
	}
	endpoint, valid := payload.(sessionregistry.Endpoint)
	if !valid {
		return nil, errors.New("approval: invalid Session endpoint reply")
	}
	if endpoint.Err != nil {
		return nil, endpoint.Err
	}
	if endpoint.Target == nil {
		return nil, errors.New("approval: Session endpoint is missing")
	}
	return &log{
		system:   source.system,
		target:   endpoint.Target,
		creation: creation,
		timeout:  source.timeout,
	}, nil
}

type log struct {
	system   goakt.ActorSystem
	target   *goakt.GrainIdentity
	creation string
	timeout  time.Duration
}

func (selected *log) Snapshot(ctx context.Context) (session.Snapshot, error) {
	payload, err := selected.system.AskGrain(ctx, selected.target, sessionactor.Read{
		CreationID: selected.creation,
	}, selected.timeout)
	if err != nil {
		return session.Snapshot{}, err
	}
	result, valid := payload.(sessionactor.View)
	if !valid {
		return session.Snapshot{}, errors.New("approval: invalid Session snapshot reply")
	}
	return result.Snapshot, result.Err
}

func (selected *log) Commit(ctx context.Context, plan session.WritePlan) (session.WriteResult, error) {
	// Audit writes are local memory commits. Wait for the actual Session reply;
	// a user cancellation must not turn a committed audit into an unknown result.
	payload, err := selected.system.AskGrain(ctx, selected.target, sessionactor.Commit{
		CreationID: selected.creation,
		Plan:       plan,
	}, time.Duration(1<<63-1))
	if err != nil {
		return session.WriteResult{}, err
	}
	result, valid := payload.(sessionactor.Committed)
	if !valid {
		return session.WriteResult{}, errors.New("approval: invalid Session commitment reply")
	}
	return result.Result, result.Err
}

var _ approvaltool.Sessions = (*Sessions)(nil)
