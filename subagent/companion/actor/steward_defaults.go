package companionactor

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	agentregistry "github.com/gorenx/goren/agent/registry"
	"github.com/gorenx/goren/settings"
	companions "github.com/gorenx/goren/subagent/companion"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// automatic is one frozen startup policy selected after reading durable
// relationships. A preparation failure remains visible under its name.
type automatic struct {
	request Create
	err     error
}

func defaultPolicies(
	ctx context.Context,
	parent agentregistry.Creation,
	existing []companions.Companion,
	view settings.Snapshot,
	creator ChildCreator,
) []automatic {
	known := make(map[string]struct{}, len(existing))
	for _, entry := range existing {
		known[entry.Name] = struct{}{}
	}
	names := make([]string, 0, len(view.Target.Companions))
	for name, policy := range view.Target.Companions {
		if !policy.Enabled {
			continue
		}
		if _, found := known[name]; !found {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	selected := make([]automatic, 0, len(names))
	for _, name := range names {
		selected = append(selected, prepareDefault(ctx, parent, name, view, creator))
	}
	return selected
}

func prepareDefault(ctx context.Context, parent agentregistry.Creation, name string, view settings.Snapshot, creator ChildCreator) automatic {
	selected := automatic{
		request: Create{
			Name: name,
			SourceRevision: view.State.Version(settings.Group{
				Section: settings.CompanionSection,
				Name:    name,
			}),
			Enabled: true,
		},
	}
	policy, found := view.Target.Companions[name]
	if !found || !policy.Enabled {
		selected.err = companions.ErrConflict
		return selected
	}
	values, found := view.Target.Agents[policy.Configuration]
	if !found {
		selected.err = companions.ErrInvalid
		return selected
	}
	selected.request.Initial, selected.err = creator.Resolve(ctx, parent, policy, values.Clone())
	return selected
}

// installDefault enters the same confirmed write and creation path as Create,
// without inventing a caller reply or resurrecting any recorded relationship.
func (owner *steward) installDefault(gc *goakt.GrainContext, selected automatic) {
	request := selected.request
	if owner.entries[request.Name] != nil {
		return
	}
	entry := &companion{
		candidate: companions.Companion{
			ParentSessionID: owner.parent.AgentID,
			Name:            request.Name,
			Enabled:         request.Enabled,
		},
		phase: Stopped,
	}
	owner.entries[request.Name] = entry
	if selected.err != nil {
		entry.failure = selected.err
		entry.phase = StartFailed
		return
	}
	owner.recordDefault(gc, entry, request)
}

func (owner *steward) recordDefault(gc *goakt.GrainContext, entry *companion, request Create) {
	next, err := newRelationship(owner.parent.AgentID, nil, request)
	if err != nil {
		entry.failure = err
		entry.phase = StartFailed
		return
	}
	plan, err := companions.NewRecord(next, 0)
	if err != nil {
		entry.failure = err
		entry.phase = StartFailed
		return
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		entry.failure = err
		entry.phase = StartFailed
		return
	}
	values := request.Initial.Clone()
	entry.mutation = &mutation{
		stage:   writePending,
		next:    next,
		plan:    plan,
		request: encoded,
		config:  &values,
	}
	owner.writeChange(gc, entry, entry.mutation)
}

// defaultPrepared settles a manual retry of configuration preparation before any
// child exists. The startup identity keeps shutdown responsible for this work.
type defaultPrepared struct {
	entry     *companion
	operation *startup
	selection automatic
}

func (owner *steward) retryDefault(gc *goakt.GrainContext, entry *companion) {
	ctx, cancel := context.WithCancel(context.Background())
	operation := &startup{
		cancel: cancel,
	}
	entry.starting = operation
	entry.phase = Starting
	entry.failure = nil
	preferences, creator, parent, name := owner.settings, owner.creator, owner.parent, entry.candidate.Name
	if err := gc.PipeToSelf(func() (any, error) {
		view, failure := preferences.Describe(ctx)
		selected := automatic{
			err: failure,
		}
		if failure == nil {
			selected = prepareDefault(ctx, parent, name, view, creator)
		}
		selected.err = errors.Join(selected.err, ctx.Err())
		return defaultPrepared{
			entry:     entry,
			operation: operation,
			selection: selected,
		}, nil
	}); err != nil {
		cancel()
		entry.starting = nil
		entry.phase = StartFailed
		entry.failure = err
	}
}

func (owner *steward) defaultPrepared(gc *goakt.GrainContext, result defaultPrepared) {
	entry := result.entry
	if entry.starting != result.operation {
		return
	}
	result.operation.cancel()
	entry.starting = nil
	if owner.state != active {
		entry.phase = Stopped
		owner.finishClose()
		return
	}
	if result.selection.err != nil {
		entry.failure = result.selection.err
		entry.phase = StartFailed
		return
	}
	owner.recordDefault(gc, entry, result.selection.request)
}
