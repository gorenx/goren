package commandactor

import (
	"context"
	"errors"
	"github.com/gorenx/goren/commands"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// Grain schedules direct commands for one Session. Command owns durable
// admission and settlement; this owner tracks only outstanding requests.
type Grain struct {
	activation *goakt.GrainProps
	binding    Initialize
	sessionID  session.SessionID
	calls      map[commands.ID]*invocation
}

type invocation struct {
	input          Execute
	parsed         commands.ParsedLine
	business       *commands.Command
	reply          *goakt.GrainReply
	handler        *goakt.GrainIdentity
	handlerPending bool
	write          *request
	cause          error
}

type requestKind uint8

const (
	registryRequest requestKind = iota
	runRequest
	handlerRequest
	historyRequest
	doneRequest
)

type request struct {
	call    *invocation
	target  *goakt.GrainIdentity
	message any
	kind    requestKind
	result  commands.Result
}

type replyReceived struct {
	request *request
	payload any
	err     error
}

type retryRequest struct{ request *request }

// OnActivate allocates no external resources.
func (owner *Grain) OnActivate(_ context.Context, props *goakt.GrainProps) error {
	owner.activation = props
	owner.calls = make(map[commands.ID]*invocation)
	return nil
}

// OnDeactivate reports interrupted delivery; it cannot certify handler cleanup.
func (owner *Grain) OnDeactivate(context.Context, *goakt.GrainProps) error {
	for _, current := range owner.calls {
		current.reply.Err(errors.New("commands: execution owner stopped"))
	}
	owner.calls = nil
	return nil
}

// OnReceive dispatches requests and I/O results without blocking a dispatcher worker.
func (owner *Grain) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case Initialize:
		if owner.binding.CreationID != "" || input.SessionID == "" || gc.Self().Name() != RuntimeName(string(input.SessionID)) || input.CreationID == "" || input.Session == nil || input.Registry == nil || input.Timeout <= 0 {
			gc.Response(Completion{
				Err: errors.New("commands: fresh binding and complete routes are required"),
			})
			return
		}
		if err := gc.EnableReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))); err != nil {
			gc.Response(Completion{
				Err: err,
			})
			return
		}
		owner.binding = input
		owner.sessionID = input.SessionID
		gc.Response(Completion{})
	case Execute:
		owner.execute(gc, input)
	case Cancel:
		owner.cancel(gc, input)
	case replyReceived:
		gc.NoErr()
		owner.receiveReply(gc, input)
	case retryRequest:
		gc.NoErr()
		if owner.calls[input.request.call.input.CommandID] == input.request.call {
			owner.send(gc, input.request)
		}
	case Close:
		if owner.binding.CreationID == "" && len(owner.calls) == 0 {
			gc.Response(Completion{})
			return
		}
		if input.CreationID != owner.binding.CreationID || owner.binding.CreationID == "" || len(owner.calls) != 0 {
			gc.Response(Completion{
				Err: errors.New("commands: matching idle execution owner is required"),
			})
			return
		}
		owner.binding.Registry = nil
		owner.binding.Session = nil
		gc.Response(Completion{})
	default:
		gc.Unhandled()
	}
}

func (owner *Grain) execute(gc *goakt.GrainContext, input Execute) {
	if owner.binding.Registry == nil || input.CreationID != owner.binding.CreationID || input.CommandID == "" || input.Options.AttachmentCount < 0 || owner.calls[input.CommandID] != nil {
		gc.Response(Executed{
			Err: errors.New("commands: invalid or duplicate execution"),
		})
		return
	}
	parsed, matched := commands.ParseCommand(input.Line)
	if !matched {
		gc.Response(Executed{})
		return
	}
	current := &invocation{
		input:  input,
		parsed: parsed,
		reply:  gc.DeferResponse(),
	}
	if current.reply == nil {
		gc.Response(Executed{
			Err: errors.New("commands: execution requires a reply-capable request"),
		})
		return
	}
	owner.calls[input.CommandID] = current
	owner.send(gc, &request{
		call:   current,
		target: owner.binding.Registry,
		message: Resolve{
			SessionID: owner.sessionID,
			Name:      parsed.Name,
		},
		kind: registryRequest,
	})
}

// send uses asynchronous framework requests for capability Actors.
// Bounded Session and registry waits return through the owning mailbox.
func (owner *Grain) send(gc *goakt.GrainContext, pending *request) {
	if pending.kind == handlerRequest {
		activation := owner.activation
		gc.RequestGrain(pending.target, pending.message,
			goakt.WithRequestTimeout(0), goakt.WithReentrancyMode(reentrancy.AllowAll),
		).Then(func(payload any, failure error) {
			// Then has no current GrainContext. Schedule the next dependency step on
			// the activation instead of retaining the recycled receiving context.
			if _, err := activation.ScheduleOnce(replyReceived{
				request: pending,
				payload: payload,
				err:     failure,
			}, 0); err != nil {
				owner.failDelivery(pending.call, err)
			}
		})
		return
	}

	system := gc.ActorSystem()
	ctx := context.WithoutCancel(gc.Context())
	timeout := owner.binding.Timeout
	if err := gc.PipeToSelf(func() (any, error) {
		payload, failure := system.AskGrain(ctx, pending.target, pending.message, timeout)
		return replyReceived{
			request: pending,
			payload: payload,
			err:     failure,
		}, nil
	}); err != nil {
		owner.failDelivery(pending.call, err)
	}
}

func (owner *Grain) failDelivery(current *invocation, failure error) {
	current.reply.Response(Executed{
		Err: failure,
	})
	current.reply = nil
	// Unknown handler or Session work remains owned until its result is known.
}

func (owner *Grain) write(gc *goakt.GrainContext, current *invocation, plan session.IdempotentWrite, kind requestKind) {
	pending := &request{
		call:   current,
		target: owner.binding.Session,
		message: sessionactor.Commit{
			CreationID: owner.binding.CreationID,
			Plan:       plan,
		},
		kind: kind,
	}
	current.write = pending
	owner.send(gc, pending)
}

func (owner *Grain) writeDone(gc *goakt.GrainContext, current *invocation) {
	if current.write != nil {
		return
	}
	plan, err := current.business.DoneWrite()
	if err != nil {
		return
	}
	owner.write(gc, current, plan, doneRequest)
}

func (owner *Grain) retire(current *invocation) {
	if current.reply == nil && !current.handlerPending && current.write == nil {
		delete(owner.calls, current.input.CommandID)
	}
}
