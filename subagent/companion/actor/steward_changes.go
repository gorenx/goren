package companionactor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/gorenx/goren/agent"
	agentregistry "github.com/gorenx/goren/agent/registry"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	"github.com/gorenx/goren/settings"
	"github.com/gorenx/goren/subagent/companion"
	goakt "github.com/tochemey/goakt/v4/actor"
)

type writeStage uint8

const (
	writePending writeStage = iota
	writeInFlight
	writeFailed
)

type mutation struct {
	failure error
	next    companion.Companion
	plan    *companion.Record
	request []byte
	replies []*goakt.GrainReply
	stage   writeStage
	config  *settings.Values
}

type recorded struct {
	entry     *resident
	operation *mutation
	err       error
}

func (operation *mutation) respond(current View, failure error) {
	for _, reply := range operation.replies {
		reply.Response(response{
			view: current,
			err:  failure,
		})
	}
	operation.replies = nil
}

func (owner *steward) create(gc *goakt.GrainContext, input Create) {
	if err := companion.ValidateName(input.Name); err != nil {
		gc.Response(response{
			err: err,
		})
		return
	}
	if input.Initial.Validate() != nil {
		gc.Response(response{
			err: companion.ErrInvalid,
		})
		return
	}
	entry := owner.entries[input.Name]
	if entry != nil && entry.mutation != nil {
		owner.change(gc, entry, input, entry.mutation.next)
		return
	}
	if entry != nil && (!entry.record.Removed || entry.starting != nil || entry.closing != nil || entry.creation.CreationID != "") {
		gc.Response(response{
			err: companion.ErrConflict,
		})
		return
	}
	next := companion.Companion{
		ParentSessionID: owner.input.Parent.AgentID,
		Name:            input.Name,
		ChildSessionID:  session.SessionID(uuid.NewString()),
		Revision:        1,
		SourceRevision:  input.SourceRevision,
		Configuration:   "companion-" + uuid.NewString(),
		Enabled:         input.Enabled,
	}
	if entry != nil {
		next.Revision = entry.record.Revision + 1
	}
	if err := next.Validate(); err != nil {
		gc.Response(response{
			err: err,
		})
		return
	}
	if entry == nil {
		entry = &resident{
			phase: Stopped,
		}
		owner.entries[input.Name] = entry
	}
	owner.change(gc, entry, input, next)
}

func (owner *steward) configure(gc *goakt.GrainContext, input Configure) {
	entry := owner.entries[input.Name]
	if entry == nil || entry.record.Removed || entry.record.Revision != input.ExpectedRevision {
		gc.Response(response{
			err: companion.ErrConflict,
		})
		return
	}
	next := entry.record.Clone()
	next.Revision++
	next.Enabled = input.Enabled
	owner.change(gc, entry, input, next)
}

func (owner *steward) remove(gc *goakt.GrainContext, input Remove) {
	entry := owner.entries[input.Name]
	if entry == nil || entry.record.Removed || entry.record.Revision != input.ExpectedRevision {
		gc.Response(response{
			err: companion.ErrConflict,
		})
		return
	}
	next := entry.record.Clone()
	next.Revision++
	next.Enabled = false
	next.Removed = true
	owner.change(gc, entry, input, next)
}

func (owner *steward) change(gc *goakt.GrainContext, entry *resident, input any, next companion.Companion) {
	encoded, err := json.Marshal(input)
	if err != nil {
		gc.Response(response{
			err: err,
		})
		return
	}
	operation := entry.mutation
	if operation != nil && !bytes.Equal(operation.request, encoded) {
		gc.Response(response{
			err: agent.ErrBusy,
		})
		return
	}
	if operation == nil {
		plan, failure := companion.NewRecord(next, entry.record.Revision)
		if failure != nil {
			gc.Response(response{
				err: failure,
			})
			return
		}
		operation = &mutation{
			stage:   writePending,
			next:    next,
			plan:    plan,
			request: encoded,
		}
		if creation, ok := input.(Create); ok {
			resolved := creation.Initial.Clone()
			operation.config = &resolved
		}
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(response{
			err: errors.New("companion: mutation requires a request"),
		})
		return
	}
	entry.mutation = operation
	operation.replies = append(operation.replies, reply)
	if operation.stage == writeInFlight {
		return
	}
	operation.stage = writeInFlight
	system := gc.ActorSystem()
	parent := owner.input.Parent
	registry := owner.input.Registry
	target := owner.input.Session
	plan := operation.plan
	configuration := owner.input.Settings
	err = gc.PipeToSelf(func() (any, error) {
		// Once admitted, a caller timeout does not abandon a potentially committed write.
		ctx := context.Background()
		accepted, failure := ask[agentregistry.Completion](ctx, system, registry, agentregistry.CheckAdmission{
			Creation: parent,
		})
		if failure == nil {
			failure = accepted.Err
		}
		if failure == nil && operation.config != nil {
			failure = saveConfiguration(ctx, configuration, operation.next.Configuration, *operation.config)
		}
		if failure == nil {
			committed, writeErr := ask[sessionactor.Committed](ctx, system, target, sessionactor.Commit{
				CreationID: parent.CreationID,
				Plan:       plan,
			})
			failure = errors.Join(writeErr, committed.Err)
			if failure == nil {
				durable, flushErr := ask[sessionactor.Flushed](ctx, system, target, sessionactor.Flush{
					CreationID: parent.CreationID,
					Barrier:    committed.Result.Barrier,
				})
				failure = errors.Join(flushErr, durable.Err)
				if failure == nil && (durable.Barrier.SessionID != parent.AgentID || durable.Barrier.NextSeq < committed.Result.Barrier.NextSeq) {
					failure = errors.New("companion: relationship durability is unconfirmed")
				}
			}
		}
		return recorded{
			entry:     entry,
			operation: operation,
			err:       failure,
		}, nil
	})
	if err != nil {
		operation.stage = writeFailed
		operation.failure = err
		operation.respond(entry.view(), err)
	}
}

// saveConfiguration verifies an earlier attempt before retrying a named create.
// A timeout may have followed a successful Settings commit; the frozen name and
// value allow retry without assigning another configuration or losing ownership.
func saveConfiguration(ctx context.Context, directory settings.Settings, name string, expected settings.Values) error {
	current, err := directory.Describe(ctx)
	if err != nil {
		return err
	}
	identifier := settings.Group{
		Section: settings.AgentSection,
		Name:    name,
	}
	if actual, found := current.Target.Agents[name]; found {
		left, err := json.Marshal(actual)
		if err != nil {
			return err
		}
		right, err := json.Marshal(expected)
		if err != nil {
			return err
		}
		if !bytes.Equal(left, right) {
			return settings.ErrConflict
		}
		return nil
	}
	if current.State.Version(identifier) != 0 {
		return settings.ErrConflict
	}
	_, err = directory.UpdateAgent(ctx, name, 0, expected)
	return err
}

func (owner *steward) recorded(gc *goakt.GrainContext, result recorded) {
	entry := result.entry
	if entry.mutation != result.operation {
		return
	}
	if result.err != nil {
		result.operation.stage = writeFailed
		result.operation.failure = result.err
		result.operation.respond(entry.view(), result.err)
		owner.finishClose()
		return
	}
	previous := entry.record
	entry.record = result.operation.next
	entry.mutation = nil
	if previous.ChildSessionID != entry.record.ChildSessionID || !previous.Enabled && entry.record.Enabled {
		entry.failure = nil
		owner.resume(gc, entry)
	} else {
		owner.reconcile(gc, entry)
	}
	result.operation.respond(entry.view(), nil)
	owner.finishClose()
}

func (owner *steward) restart(gc *goakt.GrainContext, input Restart) {
	entry := owner.entries[input.Name]
	if entry == nil || entry.record.Revision != input.ExpectedRevision || entry.record.Removed || !entry.record.Enabled || entry.creation.CreationID != input.ExpectedCreationID {
		gc.Response(response{
			err: companion.ErrConflict,
		})
		return
	}
	if entry.mutation != nil || entry.starting != nil || entry.closing != nil {
		gc.Response(response{
			err: agent.ErrBusy,
		})
		return
	}
	entry.failure = nil
	owner.resume(gc, entry)
	gc.Response(response{
		view: entry.view(),
	})
}
