package titleactor

import (
	"context"
	"errors"
	"github.com/google/uuid"

	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	"github.com/gorenx/goren/session/title"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type binding struct {
	conversation *goakt.GrainIdentity
	rules        *title.Policy
	worker       *goakt.PID
	pending      *generation
	header       session.Header
	provider     title.Provider
}

// Grain serializes title commands for its framework SessionID name.
// GoAkt stashes ordinary messages while a Session request is outstanding.
type Grain struct {
	// bindings maps CreationID to its Session and policy. Nil remembers a closed
	// creation within this activation; reactivation requires external admission.
	bindings map[string]*binding
}

// OnActivate starts without Session authority or policy.
func (owner *Grain) OnActivate(context.Context, *goakt.GrainProps) error {
	owner.bindings = make(map[string]*binding)
	return nil
}

// OnDeactivate drops policy without closing the independently owned Session.
func (owner *Grain) OnDeactivate(ctx context.Context, props *goakt.GrainProps) error {
	var failure error
	for _, selected := range owner.bindings {
		if selected != nil {
			selected.cancel()
			failure = errors.Join(failure, props.ActorSystem().Kill(ctx, selected.worker.Name()))
		}
	}
	owner.bindings = nil
	return failure
}

// OnReceive sends Session requests without blocking the actor thread.
func (owner *Grain) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case Initialize:
		owner.initialize(gc, input)
	case sessionactor.CommitNotice:
		owner.observeEvent(gc, input)
	case sessionactor.ClosedNotice:
		if RuntimeName(string(input.SessionID)) != gc.Self().Name() {
			gc.Response(Completion{
				Err: errors.New("title: closed Session does not match"),
			})
			return
		}
		owner.close(gc, input.CreationID)
	case Generate:
		owner.generate(gc, input)
	case generationFallback:
		owner.readGeneration(gc, input)
	case generationRead:
		owner.prepareGeneration(gc, input)
	case generationRecorded:
		owner.runGeneration(gc, input)
	case generationReturned:
		owner.acceptGeneration(gc, input)
	case Read:
		owner.read(gc, input)
	case Rename:
		selected, err := owner.resolve(input.CreationID)
		if err != nil {
			gc.Response(Updated{
				Err: err,
			})
			return
		}
		plan, err := selected.rules.Rename(input.Title)
		if err != nil {
			gc.Response(Updated{
				Err: err,
			})
			return
		}
		selected.cancel()
		owner.commit(gc, selected, input.CreationID, plan)
	case EnsureFallback:
		selected, err := owner.resolve(input.CreationID)
		if err != nil {
			gc.Response(Updated{
				Err: err,
			})
			return
		}
		owner.commit(gc, selected, input.CreationID, selected.rules.Fallback())
	case Close:
		owner.close(gc, input.CreationID)
	default:
		gc.Unhandled()
	}
}

func (owner *Grain) initialize(gc *goakt.GrainContext, input Initialize) {
	if input.CreationID == "" || input.Session == nil || input.Session.Name() != sessionactor.RuntimeName(string(input.Header.ID)) || RuntimeName(string(input.Header.ID)) != gc.Self().Name() {
		gc.Response(Completion{
			Err: errors.New("title: matching Session identity and creation are required"),
		})
		return
	}
	if _, found := owner.bindings[input.CreationID]; found {
		gc.Response(Completion{
			Err: errors.New("title: creation is already initialized or closed"),
		})
		return
	}
	if input.Provider != nil && (input.Provider.ID() == "" || (input.Provider.AutomaticMode() != title.AutomaticFirstPrompt && input.Provider.AutomaticMode() != title.AutomaticAllPrompts)) {
		gc.Response(Completion{
			Err: errors.New("title: invalid automatic provider"),
		})
		return
	}
	rules, err := title.NewPolicy(input.Config)
	if err == nil {
		err = gc.EnableReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.StashNonReentrant)))
	}
	if err == nil {
		var workerPID *goakt.PID
		workerPID, err = gc.ActorSystem().Spawn(gc.Context(), "title-worker-"+uuid.NewString(), &worker{})
		if err == nil {
			owner.bindings[input.CreationID] = &binding{
				header:       input.Header.Clone(),
				provider:     input.Provider,
				worker:       workerPID,
				conversation: input.Session,
				rules:        rules,
			}
		}
	}
	gc.Response(Completion{
		Err: err,
	})
}

func (owner *Grain) resolve(creationID string) (*binding, error) {
	selected := owner.bindings[creationID]
	if selected == nil {
		return nil, errors.New("title: creation is absent or closed")
	}
	return selected, nil
}

func (selected *binding) cancel() {
	if selected.pending != nil {
		selected.pending.cancel()
		selected.pending = nil
	}
}

func (owner *Grain) close(gc *goakt.GrainContext, creationID string) {
	if creationID == "" {
		gc.Response(Completion{
			Err: errors.New("title: creation identity is required"),
		})
		return
	}
	selected := owner.bindings[creationID]
	if selected == nil {
		owner.bindings[creationID] = nil
		gc.Response(Completion{})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(Completion{
			Err: errors.New("title: close requires RequestGrain"),
		})
		return
	}
	selected.cancel()
	ctx, system := context.WithoutCancel(gc.Context()), gc.ActorSystem()
	gc.RequestActor(selected.worker.Name(), drain{}, goakt.WithRequestTimeout(0)).Then(func(payload any, failure error) {
		if failure == nil {
			acknowledged, ok := payload.(Completion)
			if !ok {
				failure = errors.New("title: invalid worker drain acknowledgement")
			} else {
				failure = acknowledged.Err
			}
		}
		if failure == nil {
			failure = system.Kill(ctx, selected.worker.Name())
		}
		if failure == nil {
			owner.bindings[creationID] = nil
		}
		reply.Response(Completion{
			Err: failure,
		})
	})
}

func (owner *Grain) read(gc *goakt.GrainContext, input Read) {
	selected, err := owner.resolve(input.CreationID)
	if err != nil {
		gc.Response(View{
			Err: err,
		})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(View{
			Err: errors.New("title: read requires RequestGrain"),
		})
		return
	}
	gc.RequestGrain(selected.conversation, sessionactor.Read{
		CreationID: input.CreationID,
	}, goakt.WithRequestTimeout(0)).Then(func(payload any, failure error) {
		var accepted *title.Snapshot
		if failure == nil {
			captured, ok := payload.(sessionactor.View)
			if !ok {
				failure = errors.New("title: invalid Session read acknowledgement")
			} else if captured.Err != nil {
				failure = captured.Err
			} else {
				accepted, failure = title.Fold(captured.Snapshot.Events)
			}
		}
		reply.Response(View{
			Title: accepted,
			Err:   failure,
		})
	})
}

func (*Grain) commit(gc *goakt.GrainContext, selected *binding, creationID string, plan session.WritePlan) {
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(Updated{
			Err: errors.New("title: write requires RequestGrain"),
		})
		return
	}
	gc.RequestGrain(selected.conversation, sessionactor.Commit{
		CreationID: creationID,
		Plan:       plan,
	}, goakt.WithRequestTimeout(0)).Then(func(payload any, failure error) {
		var result session.WriteResult
		if failure == nil {
			committed, ok := payload.(sessionactor.Committed)
			if !ok {
				failure = errors.New("title: invalid Session commit acknowledgement")
			} else {
				result = committed.Result
				failure = committed.Err
			}
		}
		reply.Response(Updated{
			Result: result,
			Err:    failure,
		})
	})
}
