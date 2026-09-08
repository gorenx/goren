package titleactor

import (
	"context"
	"errors"

	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	"github.com/gorenx/goren/session/title"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// generation contains one unfinished business request, not a mailbox or queue.
type generation struct {
	creationID     string
	requestContext context.Context
	cancel         context.CancelFunc
	reply          *goakt.GrainReply
	provider       title.Provider
	refresh        bool
	events         []session.Event
	call           title.Generation
	automatic      *session.Event
}

type generationFallback struct {
	work *generation
	err  error
}

type generationRead struct {
	work *generation
	view sessionactor.View
	err  error
}

type generationRecorded struct {
	work *generation
	err  error
}

type generationReturned struct {
	work   *generation
	result title.ProviderResult
	err    error
}

func (owner *Grain) generate(gc *goakt.GrainContext, input Generate) {
	owner.beginGeneration(gc, input, nil)
}

func (owner *Grain) beginGeneration(gc *goakt.GrainContext, input Generate, automatic *session.Event) {
	selected, err := owner.resolve(input.CreationID)
	if err == nil && ((input.Provider == nil && !input.Refresh) || (input.Provider != nil && input.Provider.ID() == "")) {
		err = errors.New("title: provider is required")
	}
	if err != nil {
		gc.Response(Updated{
			Err: err,
		})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(Updated{
			Err: errors.New("title: generation requires RequestGrain"),
		})
		return
	}
	selected.cancel()
	ctx, cancel := context.WithCancel(gc.Context())
	work := &generation{
		automatic:      automatic,
		creationID:     input.CreationID,
		requestContext: ctx,
		cancel:         cancel,
		reply:          reply,
		provider:       input.Provider,
		refresh:        input.Refresh,
	}
	selected.pending = work
	owner.startGeneration(gc, work)
}

func (owner *Grain) startGeneration(gc *goakt.GrainContext, work *generation) {
	selected, err := owner.current(work)
	if err != nil {
		owner.finish(work, Updated{
			Err: err,
		})
		return
	}

	completed := make(chan generationFallback, 1)
	if err := gc.PipeToSelf(func() (any, error) { return <-completed, nil }); err != nil {
		owner.finish(work, Updated{
			Err: err,
		})
		return
	}
	gc.RequestGrain(selected.conversation, sessionactor.Commit{
		CreationID: work.creationID,
		Plan:       selected.rules.Fallback(),
	}, goakt.WithRequestTimeout(0)).Then(func(payload any, failure error) {
		if failure == nil {
			committed, ok := payload.(sessionactor.Committed)
			if !ok {
				failure = errors.New("title: invalid fallback acknowledgement")
			} else {
				failure = committed.Err
			}
		}
		completed <- generationFallback{
			work: work,
			err:  failure,
		}
	})
}

func (owner *Grain) readGeneration(gc *goakt.GrainContext, input generationFallback) {
	work := input.work
	selected, err := owner.current(work)
	if err == nil {
		err = input.err
	}
	if err != nil {
		owner.finish(work, Updated{
			Err: err,
		})
		return
	}
	completed := make(chan generationRead, 1)
	if err := gc.PipeToSelf(func() (any, error) { return <-completed, nil }); err != nil {
		owner.finish(work, Updated{
			Err: err,
		})
		return
	}
	gc.RequestGrain(selected.conversation, sessionactor.Read{
		CreationID: work.creationID,
	}, goakt.WithRequestTimeout(0)).Then(func(payload any, failure error) {
		next := generationRead{
			work: work,
			err:  failure,
		}
		if failure == nil {
			captured, ok := payload.(sessionactor.View)
			if !ok {
				next.err = errors.New("title: invalid generation snapshot")
			} else {
				next.view = captured
				next.err = captured.Err
			}
		}
		completed <- next
	})
}

func (owner *Grain) current(work *generation) (*binding, error) {
	selected, err := owner.resolve(work.creationID)
	if err != nil {
		return nil, err
	}
	if selected.pending != work {
		return nil, title.ErrSuperseded
	}
	if err := work.requestContext.Err(); err != nil {
		return nil, context.Cause(work.requestContext)
	}
	return selected, nil
}

func (owner *Grain) finish(work *generation, result Updated) {
	if selected := owner.bindings[work.creationID]; selected != nil && selected.pending == work {
		selected.pending = nil
	}
	work.cancel()
	work.reply.Response(result)
}

func (owner *Grain) prepareGeneration(gc *goakt.GrainContext, input generationRead) {
	work := input.work
	selected, err := owner.current(work)
	if err == nil {
		err = input.err
	}
	if err != nil {
		owner.finish(work, Updated{
			Err: err,
		})
		return
	}
	work.events = input.view.Snapshot.Events
	accepted, err := title.Fold(work.events)
	if err == nil && accepted != nil && accepted.Source.SourceKind() == "user" && !work.refresh {
		err = title.ErrSuperseded
	}
	if err != nil {
		owner.finish(work, Updated{
			Err: err,
		})
		return
	}
	messages, err := title.CollectMessages(work.events, nil)
	if err != nil {
		owner.finish(work, Updated{
			Err: err,
		})
		return
	}

	if work.refresh && (work.provider == nil || len(messages) == 0) {
		plan, err := selected.rules.RefreshFallback(work.events)
		if err != nil {
			owner.finish(work, Updated{
				Err: err,
			})
			return
		}
		owner.commitGeneration(gc, selected, work, plan)
		return
	}
	if work.automatic != nil {
		allowed, err := title.ShouldGenerate(selected.header, work.events, *work.automatic, work.provider.AutomaticMode())
		if err != nil {
			owner.finish(work, Updated{
				Err: err,
			})
			return
		}
		if !allowed {
			owner.finish(work, Updated{})
			return
		}
	}
	work.call, err = work.provider.Prepare(title.ProviderRequest{
		SessionID: selected.header.ID,
		Messages:  messages,
	})
	if err == nil && work.call == nil {
		err = errors.New("title: provider returned nil generation")
	}
	if err != nil {
		owner.finish(work, Updated{
			Err: err,
		})
		return
	}
	completed := make(chan generationRecorded, 1)
	if err := gc.PipeToSelf(func() (any, error) { return <-completed, nil }); err != nil {
		owner.finish(work, Updated{
			Err: err,
		})
		return
	}
	gc.RequestGrain(selected.conversation, sessionactor.Commit{
		CreationID: work.creationID,
		Plan:       session.Batch(work.call.Draft()),
	}, goakt.WithRequestTimeout(0)).Then(func(payload any, failure error) {
		if failure == nil {
			committed, ok := payload.(sessionactor.Committed)
			if !ok {
				failure = errors.New("title: invalid request log acknowledgement")
			} else {
				failure = committed.Err
			}
		}
		completed <- generationRecorded{
			work: work,
			err:  failure,
		}
	})
}

func (owner *Grain) runGeneration(gc *goakt.GrainContext, input generationRecorded) {
	work := input.work
	selected, err := owner.current(work)
	if err == nil {
		err = input.err
	}
	if err != nil {
		owner.finish(work, Updated{
			Err: err,
		})
		return
	}
	completed := make(chan generationReturned, 1)
	if err := gc.PipeToSelf(func() (any, error) { return <-completed, nil }); err != nil {
		owner.finish(work, Updated{
			Err: err,
		})
		return
	}
	gc.RequestActor(selected.worker.Name(), generate{
		requestContext: work.requestContext,
		call:           work.call,
	}, goakt.WithRequestTimeout(0), goakt.WithReentrancyMode(reentrancy.AllowAll)).Then(func(payload any, failure error) {
		next := generationReturned{
			work: work,
			err:  failure,
		}
		if failure == nil {
			result, ok := payload.(generated)
			if !ok {
				next.err = errors.New("title: invalid generation acknowledgement")
			} else {
				next.result = result.result
				next.err = result.err
			}
		}
		completed <- next
	})
}

func (owner *Grain) acceptGeneration(gc *goakt.GrainContext, input generationReturned) {
	work := input.work
	selected, err := owner.current(work)
	if err == nil {
		err = input.err
	}
	if err != nil {
		owner.finish(work, Updated{
			Err: err,
		})
		return
	}
	plan, err := selected.rules.Accept(work.events, work.provider.ID(), input.result, work.refresh)
	if err != nil {
		owner.finish(work, Updated{
			Err: err,
		})
		return
	}
	owner.commitGeneration(gc, selected, work, plan)
}

func (owner *Grain) commitGeneration(gc *goakt.GrainContext, selected *binding, work *generation, plan session.WritePlan) {
	gc.RequestGrain(selected.conversation, sessionactor.Commit{
		CreationID: work.creationID,
		Plan:       plan,
	}, goakt.WithRequestTimeout(0)).Then(func(payload any, failure error) {
		result := Updated{
			Err: failure,
		}
		if failure == nil {
			committed, ok := payload.(sessionactor.Committed)
			if !ok {
				result.Err = errors.New("title: invalid title commit acknowledgement")
			} else {
				result.Result = committed.Result
				result.Err = committed.Err
			}
		}
		owner.finish(work, result)
	})
}
