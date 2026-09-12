package companionactor

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	agentactor "github.com/gorenx/goren/agent/actor"
	agentregistry "github.com/gorenx/goren/agent/registry"
	loopactor "github.com/gorenx/goren/agentloop/actor"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	"github.com/gorenx/goren/settings"
	companions "github.com/gorenx/goren/subagent/companion"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// InstallerName identifies the Companion installer in the ActorSystem.
const InstallerName = "companions"

// Dependencies are the capabilities borrowed by Installer; their owners retain
// configuration and resource lifetimes.
type Dependencies struct {
	// Registry is the Agent Registry endpoint for admission, readiness and resource closure.
	Registry *goakt.GrainIdentity
	// Settings owns named configuration; Installer borrows it for Steward.
	Settings settings.Settings
	// Creator enters ordinary child creation or recovery using a configuration name.
	Creator ChildCreator
}

// InspectInstallation reads Companion installation and shutdown status for a parent.
type InspectInstallation struct {
	// Parent identifies the exact installation; a replacement lifetime cannot reuse the query.
	Parent agentregistry.Creation
}

type releaseStage uint8

const (
	releaseRequired releaseStage = iota
	stewardClosed
	grainStopped
)

// installation is Companion support bound to one exact parent lifetime. It
// retains the routes and outstanding deactivation obligation; Steward owns the
// Companion relationships and their execution progress.
type installation struct {
	// parent identifies the lifetime that owns this Companion installation.
	parent agentregistry.Creation
	// role is the admitted parent role checked on repeated installation and shutdown.
	role agent.Role
	// agent is the parent AgentLoop endpoint from StartComponent.Agent, retained for retry validation.
	agent *goakt.GrainIdentity
	// session is the parent Session endpoint for retry validation; absent when stop precedes install.
	session *goakt.GrainIdentity
	// steward is the management endpoint, deactivated only after business closure is confirmed.
	steward *goakt.GrainIdentity
	// stage retains confirmed progress: closure required, Steward closed, or Grain stopped.
	stage releaseStage
	// operation is the active release attempt; concurrent stop requests join its waiters.
	operation *release
	// failure retains the last shutdown error so the installation can retry remaining release work.
	failure error
	// waiters share this installation's stop result and are cleared after success or failure.
	waiters []*goakt.GrainReply
}

// release is one in-flight attempt to discharge an installation's obligation.
type release struct {
	// cancel ends this release attempt's wait without certifying business resource release.
	cancel context.CancelFunc
}

type released struct {
	// entry identifies the original installation so late results cannot affect a replacement.
	entry *installation
	// operation identifies the attempt; only the installation's current attempt may settle it.
	operation *release
	// stage records actual release progress, including checkpoints confirmed before failure.
	stage releaseStage
	// err reports Steward closure or Grain deactivation failure and preserves remaining responsibility.
	err error
}

// Installer installs and deactivates Companion support for each ordinary parent.
// Steward owns Companion management; this adapter retains only installation and
// release responsibility while using the ordinary Agent component protocol.
type Installer struct {
	// dependencies are borrowed once from composition and cannot be replaced by installation requests.
	dependencies *Dependencies
	// installations retains per-parent routing and release responsibility, fenced by CreationID.
	installations map[session.SessionID]*installation
}

// Register provides the installer endpoint before any parent is admitted.
// Companion installation begins only when Agent Registry sends StartComponent.
func Register(ctx context.Context, system goakt.ActorSystem, input Dependencies) (*goakt.GrainIdentity, error) {
	if ctx == nil || system == nil {
		return nil, companions.ErrInvalid
	}
	target, err := goakt.GrainOf[*Installer](ctx, system, InstallerName,
		goakt.WithLongLivedGrain(),
		goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))),
	)
	if err != nil {
		return nil, err
	}
	result, err := ask[response](ctx, system, target, input)
	if err != nil {
		return nil, err
	}
	return target, result.err
}

// OnActivate waits for composition to provide the borrowed capabilities.
func (*Installer) OnActivate(context.Context, *goakt.GrainProps) error { return nil }

// OnDeactivate releases request waiters without claiming business cleanup.
func (owner *Installer) OnDeactivate(context.Context, *goakt.GrainProps) error {
	for _, entry := range owner.installations {
		if entry.operation != nil {
			entry.operation.cancel()
		}
		entry.respond(errors.New("companion: installer stopped during release"))
	}
	return nil
}

// OnReceive forwards preparation to Steward and serializes framework release.
func (owner *Installer) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case Dependencies:
		var failure error
		if owner.dependencies != nil || input.Registry == nil ||
			input.Settings == nil || input.Creator == nil {
			failure = companions.ErrInvalid
		} else {
			owner.dependencies = &input
		}
		gc.Response(response{
			err: failure,
		})
	case agentactor.StartComponent:
		owner.install(gc, input)
	case agentactor.StopComponent:
		owner.stop(gc, input)
	case released:
		if input.entry.operation != input.operation {
			return
		}
		input.operation.cancel()
		input.entry.operation = nil
		input.entry.stage = input.stage
		input.entry.failure = input.err
		input.entry.respond(input.err)
	case InspectInstallation:
		owner.inspect(gc, input.Parent)
	default:
		gc.Unhandled()
	}
}

func (owner *Installer) install(gc *goakt.GrainContext, input agentactor.StartComponent) {
	if owner.dependencies == nil ||
		input.AgentID == "" ||
		input.CreationID == "" ||
		input.Agent == nil ||
		input.Session == nil ||
		!input.Role.Valid() ||
		input.Agent.Name() != loopactor.RuntimeName(string(input.AgentID)) ||
		input.Session.Name() != sessionactor.RuntimeName(string(input.AgentID)) {
		gc.Response(agentactor.ComponentStarted{
			Err: companions.ErrInvalid,
		})
		return
	}
	if input.Role == agent.CompanionRole {
		gc.Response(agentactor.ComponentStarted{
			Runtime: input.Agent,
		})
		return
	}
	entry := owner.installations[input.AgentID]
	if entry != nil && (entry.stage != grainStopped || entry.parent.CreationID == input.CreationID) {
		if entry.parent.CreationID != input.CreationID ||
			entry.role != input.Role || entry.stage != releaseRequired ||
			entry.operation != nil || entry.failure != nil ||
			!entry.agent.Equal(input.Agent) || !entry.session.Equal(input.Session) {
			gc.Response(agentactor.ComponentStarted{
				Err: agentregistry.ErrCreation,
			})
			return
		}
	} else {
		target, err := stewardRoute(
			gc.Context(),
			gc.ActorSystem(),
			agentregistry.Creation{
				AgentID:    input.AgentID,
				CreationID: input.CreationID,
			},
		)
		if err != nil {
			gc.Response(agentactor.ComponentStarted{
				Err: err,
			})
			return
		}
		entry = &installation{
			parent: agentregistry.Creation{
				AgentID:    input.AgentID,
				CreationID: input.CreationID,
			},
			role:    input.Role,
			agent:   input.Agent,
			session: input.Session,
			steward: target,
		}
		if owner.installations == nil {
			owner.installations = make(map[session.SessionID]*installation)
		}
		owner.installations[input.AgentID] = entry
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(agentactor.ComponentStarted{
			Err: companions.ErrInvalid,
		})
		return
	}
	gc.RequestGrain(
		entry.steward,
		start{
			Parent: agentregistry.Creation{
				AgentID:    input.AgentID,
				CreationID: input.CreationID,
			},
			Registry: owner.dependencies.Registry,
			Session:  input.Session,
			Settings: owner.dependencies.Settings,
			Creator:  owner.dependencies.Creator,
		},
		goakt.WithRequestTimeout(0),
		goakt.WithReentrancyMode(reentrancy.AllowAll),
	).Then(
		func(payload any, failure error) {
			if failure == nil {
				accepted, ok := payload.(response)
				if !ok {
					failure = errors.New("companion: invalid installation reply")
				} else {
					failure = accepted.err
				}
			}
			reply.Response(agentactor.ComponentStarted{
				Runtime: entry.steward,
				Err:     failure,
			})
		})
}

func (owner *Installer) inspect(gc *goakt.GrainContext, parent agentregistry.Creation) {
	entry := owner.installations[parent.AgentID]
	if entry == nil || entry.parent.CreationID != parent.CreationID {
		gc.Response(Status{
			Err: agentregistry.ErrCreation,
		})
		return
	}
	if entry.stage == grainStopped {
		gc.Response(Status{
			Phase: Stopped,
		})
		return
	}
	if entry.operation != nil || entry.failure != nil || entry.stage == stewardClosed {
		phase := Stopping
		if entry.failure != nil {
			phase = CloseFailed
		}
		gc.Response(Status{
			Phase: phase,
			Err:   entry.failure,
		})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(Status{
			Err: companions.ErrInvalid,
		})
		return
	}
	gc.RequestGrain(entry.steward, command{
		parent: parent,
		input:  inspection{},
	},
		goakt.WithRequestTimeout(0), goakt.WithReentrancyMode(reentrancy.AllowAll),
	).Then(func(payload any, failure error) {
		current := Status{}
		if failure == nil {
			result, ok := payload.(response)
			if !ok {
				failure = errors.New("companion: invalid status reply")
			} else {
				current = result.status
				failure = result.err
			}
		}
		if failure != nil {
			current.Err = failure
		}
		reply.Response(current)
	})
}

func (owner *Installer) stop(gc *goakt.GrainContext, input agentactor.StopComponent) {
	if input.AgentID == "" || input.CreationID == "" || !input.Role.Valid() {
		gc.Response(agentactor.ComponentStopped{
			Err: companions.ErrInvalid,
		})
		return
	}
	if input.Role == agent.CompanionRole {
		gc.Response(agentactor.ComponentStopped{})
		return
	}
	entry := owner.installations[input.AgentID]
	if entry == nil {
		if owner.installations == nil {
			owner.installations = make(map[session.SessionID]*installation)
		}
		owner.installations[input.AgentID] = &installation{
			parent: agentregistry.Creation{
				AgentID:    input.AgentID,
				CreationID: input.CreationID,
			},
			role:  input.Role,
			stage: grainStopped,
		}
		gc.Response(agentactor.ComponentStopped{})
		return
	}
	if entry.parent.CreationID != input.CreationID || entry.role != input.Role {
		gc.Response(agentactor.ComponentStopped{
			Err: agentregistry.ErrCreation,
		})
		return
	}
	if entry.stage == grainStopped {
		gc.Response(agentactor.ComponentStopped{})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(agentactor.ComponentStopped{
			Err: companions.ErrInvalid,
		})
		return
	}
	entry.waiters = append(entry.waiters, reply)
	if entry.operation != nil {
		return
	}
	owner.release(gc, entry)
}

func (owner *Installer) release(gc *goakt.GrainContext, entry *installation) {
	ctx, cancel := context.WithCancel(context.Background())
	operation := &release{
		cancel: cancel,
	}
	entry.operation = operation
	system := gc.ActorSystem()
	target := entry.steward
	parent := entry.parent
	stage := entry.stage
	err := gc.PipeToSelf(func() (any, error) {
		result := released{
			entry:     entry,
			operation: operation,
			stage:     stage,
		}
		if stage == releaseRequired {
			closed, failure := ask[response](ctx, system, target, command{
				parent: parent,
				input:  closing{},
			})
			result.err = errors.Join(failure, closed.err)
			if result.err == nil {
				result.stage = stewardClosed
			}
		}
		if result.err == nil {
			result.err = system.TellGrain(ctx, target, new(goakt.PoisonPill))
			if result.err == nil {
				result.stage = grainStopped
			}
		}
		return result, nil
	})
	if err != nil {
		cancel()
		entry.operation = nil
		entry.failure = err
		entry.respond(err)
	}
}

func (entry *installation) respond(failure error) {
	for _, reply := range entry.waiters {
		reply.Response(agentactor.ComponentStopped{
			Err: failure,
		})
	}
	entry.waiters = nil
}
