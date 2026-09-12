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
	companions "github.com/gorenx/goren/subagent/companion"
	goakt "github.com/tochemey/goakt/v4/actor"
)

type writeStage uint8

const (
	writePending writeStage = iota
	writeInFlight
	writeFailed
)

// mutation is a management change whose frozen intent and replies survive
// failed persistence attempts until the same change can settle.
type mutation struct {
	// failure retains the last configuration or relationship persistence error for queries and retry.
	failure error
	// next is the frozen target relationship, published only after persistence is confirmed.
	next companions.Companion
	// plan reuses the same Session WritePlan so retries locate the original commit without duplicate writes.
	plan *companions.Record
	// request encodes the original management input for identical-request and conflict checks.
	request []byte
	// replies share this change's success or failure and are settled together.
	replies []*goakt.GrainReply
	// stage serializes persistence attempts and permits retry after failure.
	stage writeStage
	// config is the detached initial configuration to save; enablement-only changes leave it nil.
	config *settings.Values
}

type recorded struct {
	// entry identifies the Companion whose persistence attempt is settling.
	entry *companion
	// operation must still match the current mutation before its relationship can be published.
	operation *mutation
	// err reports Settings save, relationship Commit or Flush failure; failure never publishes the target.
	err error
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
	if err := companions.ValidateName(input.Name); err != nil {
		gc.Response(response{
			err: err,
		})
		return
	}
	if input.Initial.Validate() != nil {
		gc.Response(response{
			err: companions.ErrInvalid,
		})
		return
	}
	entry := owner.entries[input.Name]
	if entry != nil && entry.mutation != nil {
		owner.change(gc, entry, input, entry.mutation.next)
		return
	}
	if entry != nil &&
		(entry.relationship.Name != "" && !entry.relationship.Removed ||
			entry.starting != nil ||
			entry.closing != nil ||
			entry.creation.CreationID != "") {
		gc.Response(response{
			err: companions.ErrConflict,
		})
		return
	}
	next, err := newRelationship(owner.parent.AgentID, entry, input)
	if err != nil {
		gc.Response(response{
			err: err,
		})
		return
	}
	if entry == nil {
		entry = &companion{
			phase: Stopped,
		}
		owner.entries[input.Name] = entry
	}
	owner.change(gc, entry, input, next)
}

func newRelationship(parent session.SessionID, previous *companion, input Create) (companions.Companion, error) {
	next := companions.Companion{
		ParentSessionID: parent,
		Name:            input.Name,
		ChildSessionID:  session.SessionID(uuid.NewString()),
		Revision:        1,
		SourceRevision:  input.SourceRevision,
		Configuration:   "companion-" + uuid.NewString(),
		Enabled:         input.Enabled,
	}
	if previous != nil {
		next.Revision = previous.relationship.Revision + 1
	}
	return next, next.Validate()
}

func (owner *steward) configure(gc *goakt.GrainContext, input Configure) {
	entry := owner.entries[input.Name]
	if entry == nil || entry.relationship.Removed || entry.relationship.Revision != input.ExpectedRevision {
		gc.Response(response{
			err: companions.ErrConflict,
		})
		return
	}
	next := entry.relationship.Clone()
	next.Revision++
	next.Enabled = input.Enabled
	owner.change(gc, entry, input, next)
}

func (owner *steward) remove(gc *goakt.GrainContext, input Remove) {
	entry := owner.entries[input.Name]
	if entry == nil || entry.relationship.Removed || entry.relationship.Revision != input.ExpectedRevision {
		gc.Response(response{
			err: companions.ErrConflict,
		})
		return
	}
	next := entry.relationship.Clone()
	next.Revision++
	next.Enabled = false
	next.Removed = true
	owner.change(gc, entry, input, next)
}

func (owner *steward) change(gc *goakt.GrainContext, entry *companion, input any, next companions.Companion) {
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
		plan, failure := companions.NewRecord(next, entry.relationship.Revision)
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
	owner.writeChange(gc, entry, operation)
}

func (owner *steward) writeChange(gc *goakt.GrainContext, entry *companion, operation *mutation) {
	if operation.stage == writeInFlight {
		return
	}
	operation.stage = writeInFlight
	system := gc.ActorSystem()
	parent := owner.parent
	registry := owner.registry
	target := owner.session
	plan := operation.plan
	configuration := owner.settings
	err := gc.PipeToSelf(func() (any, error) {
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
				if failure == nil &&
					(durable.Barrier.SessionID != parent.AgentID ||
						durable.Barrier.NextSeq < committed.Result.Barrier.NextSeq) {
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
	previous := entry.relationship
	entry.relationship = result.operation.next
	entry.mutation = nil
	if previous.ChildSessionID != entry.relationship.ChildSessionID || !previous.Enabled && entry.relationship.Enabled {
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
	if entry == nil {
		gc.Response(response{
			err: companions.ErrConflict,
		})
		return
	}
	observed := entry.view().Companion
	if observed.Revision != input.ExpectedRevision || observed.Removed || !observed.Enabled || entry.creation.CreationID != input.ExpectedCreationID {
		gc.Response(response{
			err: companions.ErrConflict,
		})
		return
	}
	if entry.starting != nil || entry.closing != nil ||
		entry.mutation != nil && (entry.relationship.Name != "" || entry.mutation.stage != writeFailed) {
		gc.Response(response{
			err: agent.ErrBusy,
		})
		return
	}
	switch {
	case entry.mutation != nil:
		// Retry the original creation intent; an uncertain Flush must not
		// allocate another Session or append a second relationship event.
		owner.writeChange(gc, entry, entry.mutation)
	case entry.relationship.Name == "":
		owner.retryDefault(gc, entry)
	default:
		entry.failure = nil
		owner.resume(gc, entry)
	}
	gc.Response(response{
		view: entry.view(),
	})
}

// restartConfiguredChild uses the existing child closure and recovery flow.
// An in-flight startup settles before closure; a repeated notice joins that work.
func (owner *steward) restartConfiguredChild(gc *goakt.GrainContext, input agentregistry.RestartChild) {
	if owner.state != active {
		gc.Response(agentregistry.Completion{Err: agent.ErrClosing})
		return
	}
	for _, entry := range owner.entries {
		if entry.creation != input.Creation {
			continue
		}
		if entry.relationship.Removed || !entry.relationship.Enabled {
			gc.Response(agentregistry.Completion{})
			return
		}
		if entry.closing == nil {
			if entry.starting != nil {
				entry.phase = Stopping
			} else {
				entry.failure = nil
				owner.closeChild(gc, entry, false)
			}
		}
		gc.Response(agentregistry.Completion{})
		return
	}
	gc.Response(agentregistry.Completion{Err: agentregistry.ErrCreation})
}
