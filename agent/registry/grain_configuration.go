package registry

import (
	"context"
	"errors"
	"reflect"

	"github.com/google/uuid"
	"github.com/gorenx/goren/agent"
	agentactor "github.com/gorenx/goren/agent/actor"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/settings"
	settingsactor "github.com/gorenx/goren/settings/actor"
	toolregistry "github.com/gorenx/goren/tools/registry"
	catalogactor "github.com/gorenx/goren/tools/registry/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// configuration is the latest saved input observed by the lifecycle owner.
// Running components retain their initialization values until they are closed.
type configuration struct {
	revision  int64
	directory settings.Directory
}

// restart retains the original creation request while its resources are closed.
// New creation identities are allocated only after successful cleanup.
type restart struct {
	input   CreateAgent
	failure error
	replies []*goakt.GrainReply
}

type configurationPrepared struct {
	operation *activation
	input     CreateAgent
	snapshot  settings.Snapshot
	values    *settings.Values
	err       error
}

type restartClose struct{ identity Creation }

func (owner *Grain) prepareCreation(gc *goakt.GrainContext, operation *activation) {
	input := operation.input.clone()
	if input.Settings == nil {
		owner.installCreation(gc, operation, input)
		return
	}
	if gc.ActorSystem().TopicActor() == nil {
		owner.finishCreation(gc, operation, errors.New("agent registry: Settings observation requires PubSub"))
		return
	}
	var subscribed <-chan struct{}
	if owner.observer == nil {
		ready := make(chan struct{})
		listener, err := gc.ActorSystem().Spawn(
			gc.Context(),
			Name+".settings",
			&configurationObserver{
				target: gc.Self(),
				ready:  ready,
			},
			goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))),
		)
		if err != nil {
			owner.finishCreation(gc, operation, err)
			return
		}
		owner.observer = listener
		subscribed = ready
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(gc.Context()))
	operation.cancel = cancel
	running := gc.ActorSystem()
	err := gc.PipeToSelf(func() (any, error) {
		result := configurationPrepared{
			operation: operation,
			input:     input,
		}
		if subscribed != nil {
			select {
			case <-subscribed:
			case <-ctx.Done():
				result.err = ctx.Err()
				return result, nil
			}
		}
		result.err = prepareConfiguration(ctx, running, &result)
		return result, nil
	})
	if err != nil {
		owner.finishCreation(gc, operation, err)
	}
}

// prepareConfiguration resolves one snapshot before any runtime component opens.
// Registry schemas provide implementations; Settings supplies editable values.
func prepareConfiguration(ctx context.Context, running goakt.ActorSystem, result *configurationPrepared) error {
	input := &result.input
	payload, err := running.AskGrain(ctx, input.Settings, settingsactor.Describe{}, input.Timeout)
	if err != nil {
		return err
	}
	view, ok := payload.(settingsactor.Result)
	if !ok {
		return errors.New("agent registry: invalid Settings snapshot")
	}
	if view.Err != nil {
		return view.Err
	}
	result.snapshot = view.Snapshot
	values, err := configurationValues(view.Snapshot.Target, input.ConfigurationName)
	if err != nil {
		return err
	}
	result.values = &values
	input.AgentOptions = &values.Model
	prompt := values.Prompts["default"].Prompt()
	input.Prompt = &prompt
	payload, err = running.AskGrain(ctx, input.ToolsRegistry, catalogactor.ReadCatalog{}, input.Timeout)
	if err != nil {
		return err
	}
	installed, ok := payload.(toolregistry.Catalog)
	if !ok {
		return errors.New("agent registry: invalid tool catalog")
	}
	scoped := toolregistry.Configuration{
		Global: installed,
		Custom: toolregistry.Catalog{InheritGlobal: true},
	}
	if input.Tools != nil {
		if input.Tools.InheritGlobal {
			scoped.Custom = input.Tools.Clone()
		} else {
			scoped.Global = input.Tools.Clone()
			scoped.Custom = toolregistry.Catalog{}
		}
	}
	selected, err := scoped.Select(installed, values.Tools, values.MCPConnections())
	if err != nil {
		return err
	}
	input.configuration = &selected
	return nil
}

func configurationValues(directory settings.Directory, name string) (settings.Values, error) {
	if name == "" {
		return directory.Values.Clone(), nil
	}
	values, found := directory.Agents[name]
	if !found {
		return settings.Values{}, errors.New("agent registry: named configuration is absent")
	}
	return values.Clone(), nil
}

func (owner *Grain) receiveConfiguration(gc *goakt.GrainContext) bool {
	switch input := gc.Message().(type) {
	case configurationPrepared:
		gc.NoErr()
		operation := input.operation
		entry := owner.creations[operation.input.Creation.AgentID]
		if entry.activation != operation || operation.cancel == nil {
			return true
		}
		operation.cancel()
		operation.cancel = nil
		if input.err != nil {
			owner.finishCreation(gc, operation, input.err)
			return true
		}
		if err := owner.directory.CheckAdmission(operation.input.Creation.AgentID); err != nil {
			owner.finishCreation(gc, operation, err)
			return true
		}
		if owner.preferences != nil && owner.preferences.revision > input.snapshot.State.Revision {
			owner.prepareCreation(gc, operation)
			return true
		}
		owner.preferences = &configuration{
			revision:  input.snapshot.State.Revision,
			directory: input.snapshot.Target.Clone(),
		}
		operation.values = input.values
		owner.installCreation(gc, operation, input.input)
	case settingsactor.ChangeNotice:
		gc.Response(Completion{})
		if owner.observer == nil || owner.preferences != nil && input.Revision <= owner.preferences.revision {
			return true
		}
		owner.preferences = &configuration{
			revision:  input.Revision,
			directory: input.Configuration(),
		}
		owner.restartChanged(gc)
	case restartPrepared:
		gc.NoErr()
		owner.startReplacement(gc, input)
	case restartCreate:
		if owner.restarts[input.input.Creation.AgentID] != input.operation {
			gc.Response(Ready{Err: agent.ErrClosing})
			return true
		}
		owner.createAgent(gc, input.input)
	case restartClose:
		owner.closeAgent(gc, CloseAgent{Creation: input.identity})
	default:
		return false
	}
	return true
}

func (owner *Grain) restartChanged(gc *goakt.GrainContext) {
	if owner.preferences == nil {
		return
	}
	for identifier, entry := range owner.creations {
		if owner.directory.IsClosed(identifier) || entry.activation == nil || entry.activation.values == nil {
			continue
		}
		values, err := configurationValues(owner.preferences.directory, entry.activation.input.ConfigurationName)
		if err == nil && reflect.DeepEqual(*entry.activation.values, values) {
			continue
		}
		if owner.directory.CheckAdmission(identifier) != nil {
			continue
		}
		if manager := entry.activation.input.Manager; manager != nil {
			identity := entry.activation.input.Creation
			logger := gc.ActorSystem().Logger()
			gc.RequestGrain(manager, RestartChild{Creation: identity}, goakt.WithRequestTimeout(0), goakt.WithReentrancyMode(reentrancy.AllowAll)).Then(func(payload any, failure error) {
				result, valid := payload.(Completion)
				if failure != nil || !valid || result.Err != nil {
					logger.Error("agent registry: child configuration restart was not admitted")
				}
			})
		} else {
			owner.restartAgent(gc, identifier)
		}
	}
}

func (owner *Grain) restartAgent(gc *goakt.GrainContext, identifier session.SessionID) {
	if owner.restarts[identifier] != nil {
		return
	}
	entry := owner.creations[identifier]
	if entry.activation == nil || owner.directory.CheckAdmission(identifier) != nil {
		return
	}
	operation := &restart{input: entry.activation.input.clone()}
	if owner.restarts == nil {
		owner.restarts = make(map[session.SessionID]*restart)
	}
	owner.restarts[identifier] = operation
	owner.closeForRestart(gc, operation)
}

type restartPrepared struct {
	operation *restart
	input     CreateAgent
	err       error
}

type restartCreate struct {
	operation *restart
	input     CreateAgent
}

func (owner *Grain) closeForRestart(gc *goakt.GrainContext, operation *restart) {
	identifier := operation.input.Creation.AgentID
	requestComponent(gc, gc.Self(), restartClose{identity: operation.input.Creation},
		func(gc *goakt.GrainContext, result agentactor.Closed, failure error) {
			if owner.restarts[identifier] != operation {
				return
			}
			failure = errors.Join(failure, result.Err)
			if failure != nil {
				owner.failRestart(operation, failure)
				return
			}
			ctx := context.WithoutCancel(gc.Context())
			err := gc.PipeToSelf(func() (any, error) {
				replacement := operation.input.clone()
				replacement.Creation.CreationID = uuid.NewString()
				stored, err := replacement.Storage.Load(ctx, identifier)
				replacement.Restore = stored != nil
				if replacement.Restore {
					replacement.Options = session.CreateOptions{}
				}
				return restartPrepared{
					operation: operation,
					input:     replacement,
					err:       err,
				}, nil
			})
			if err != nil {
				owner.failRestart(operation, err)
			}
		},
	)
}

func (owner *Grain) failRestart(operation *restart, failure error) {
	operation.failure = failure
	operation.respond(Ready{Err: failure})
}

func (owner *Grain) startReplacement(gc *goakt.GrainContext, input restartPrepared) {
	operation := input.operation
	identifier := input.input.Creation.AgentID
	if owner.restarts[identifier] != operation {
		return
	}
	if input.err != nil {
		owner.failRestart(operation, input.err)
		return
	}
	requestComponent(gc, gc.Self(), restartCreate{
		operation: operation,
		input:     input.input,
	}, func(gc *goakt.GrainContext, result Ready, failure error) {
		if owner.restarts[identifier] != operation {
			return
		}
		failure = errors.Join(failure, result.Err)
		if failure != nil {
			owner.failRestart(operation, failure)
			return
		}
		delete(owner.restarts, identifier)
		operation.respond(result)
		owner.restartChanged(gc)
	})
}

// configurationObserver only forwards durable Settings snapshots to Registry.
// It never owns Agent state, interprets configuration, or waits for a Turn.
type configurationObserver struct {
	target *goakt.GrainIdentity
	ready  chan struct{}
}

func (*configurationObserver) PreStart(*goakt.Context) error { return nil }
func (*configurationObserver) PostStop(*goakt.Context) error { return nil }
func (listener *configurationObserver) Receive(rc *goakt.ReceiveContext) {
	switch input := rc.Message().(type) {
	case *goakt.PostStart:
		rc.Tell(rc.ActorSystem().TopicActor(), goakt.NewSubscribe(settingsactor.ChangedTopic))
	case *goakt.SubscribeAck:
		if input.Topic() == settingsactor.ChangedTopic && listener.ready != nil {
			close(listener.ready)
			listener.ready = nil
		}
	case settingsactor.ChangeNotice:
		pending := rc.RequestGrain(listener.target, input, goakt.WithRequestTimeout(0))
		logger := rc.Logger()
		if pending != nil {
			pending.Then(func(_ any, err error) {
				if err != nil {
					logger.Error("agent registry: Settings notification delivery failed")
				}
			})
		}
	}
}

func (operation *restart) respond(result Ready) {
	for _, reply := range operation.replies {
		reply.Response(result)
	}
	operation.replies = nil
}

func (owner *Grain) cancelRestarts(identifier session.SessionID) {
	for name, operation := range owner.restarts {
		current := name
		for {
			if identifier == "" || current == identifier {
				delete(owner.restarts, name)
				operation.respond(Ready{Err: agent.ErrClosing})
				break
			}
			entry, found := owner.directory.Get(current)
			if !found || entry.ParentID == nil {
				break
			}
			current = *entry.ParentID
		}
	}
}
