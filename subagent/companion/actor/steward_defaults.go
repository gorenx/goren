package companionactor

import (
	"context"
	"encoding/json"
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
		policy := view.Target.Companions[name]
		values := view.Target.Agents[policy.Configuration].Clone()
		resolved, err := creator.Resolve(ctx, parent, policy, values)
		selected = append(selected, automatic{
			request: Create{
				Name: name,
				SourceRevision: view.State.Version(settings.Group{
					Section: settings.CompanionSection,
					Name:    name,
				}),
				Enabled: true,
				Initial: resolved,
			},
			err: err,
		})
	}
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
