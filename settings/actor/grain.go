package settingsactor

import (
	"context"
	"errors"

	"github.com/gorenx/goren/settings"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Name identifies the deployment's single Settings service in GoAkt.
const Name = "settings"

// ErrUnavailable identifies an absent or closing settings owner.
var ErrUnavailable = errors.New("settings: service is unavailable")

// Grain serializes business mutations across asynchronous I/O. While a task
// owns the Service, queries use the detached view from its last completed call.
type Grain struct {
	service *settings.Service
	view    settings.Snapshot
	queue   []*request
	active  *request
	closing bool
	closed  bool
	closers []*goakt.GrainReply
}

type request struct {
	command command
	reply   *goakt.GrainReply
}

type response struct {
	request *request
	result  Result
}

// OnActivate waits for composition to supply the business service.
func (*Grain) OnActivate(context.Context, *goakt.GrainProps) error { return nil }

// OnDeactivate must follow a successful Close before the database is released.
func (owner *Grain) OnDeactivate(context.Context, *goakt.GrainProps) error {
	if owner.active != nil {
		return errors.New("settings: active requests must drain before deactivation")
	}
	return nil
}

// OnReceive never executes storage or capability I/O in the mailbox.
func (owner *Grain) OnReceive(gc *goakt.GrainContext) {
	switch incoming := gc.Message().(type) {
	case Initialize:
		if owner.service != nil || incoming.Service == nil {
			gc.Response(Result{
				Err: ErrUnavailable,
			})
			return
		}
		owner.service = incoming.Service
		owner.view = incoming.Service.Snapshot()
		gc.Response(Result{
			Snapshot: owner.view.Clone(),
		})
	case Describe:
		if owner.service == nil || owner.closed {
			gc.Response(Result{
				Err: ErrUnavailable,
			})
			return
		}
		gc.Response(Result{
			Snapshot: owner.view.Clone(),
		})
	case command:
		if owner.service == nil ||
			owner.closing ||
			owner.closed {
			gc.Response(Result{
				Err: ErrUnavailable,
			})
			return
		}
		if incoming.context() == nil {
			gc.Response(Result{
				Err: settings.ErrInvalid,
			})
			return
		}
		retained := gc.DeferResponse()
		if retained == nil {
			gc.Response(Result{
				Err: errors.New("settings: asynchronous request required"),
			})
			return
		}
		owner.queue = append(owner.queue, &request{
			command: incoming,
			reply:   retained,
		})
		owner.dispatch(gc)
	case response:
		if owner.active != incoming.request {
			return
		}
		owner.view = incoming.result.Snapshot.Clone()
		owner.active.reply.Response(incoming.result)
		owner.active = nil
		owner.dispatch(gc)
	case Close:
		if owner.closed {
			gc.Response(Result{})
			return
		}
		owner.closing = true
		if owner.active == nil && len(owner.queue) == 0 {
			owner.closed = true
			gc.Response(Result{})
			return
		}
		retained := gc.DeferResponse()
		if retained == nil {
			gc.Response(Result{
				Err: errors.New("settings: asynchronous close required"),
			})
			return
		}
		owner.closers = append(owner.closers, retained)
	default:
		gc.Unhandled()
	}
}

func (owner *Grain) dispatch(gc *goakt.GrainContext) {
	if owner.active != nil {
		return
	}
	if len(owner.queue) == 0 {
		if owner.closing {
			owner.closed = true
			for _, retained := range owner.closers {
				retained.Response(Result{})
			}
			owner.closers = nil
		}
		return
	}
	pending := owner.queue[0]
	owner.queue = owner.queue[1:]
	owner.active = pending
	business := owner.service
	err := gc.PipeToSelf(func() (payload any, taskErr error) {
		output := response{
			request: pending,
		}
		defer func() {
			if recover() != nil {
				output.result = Result{
					Snapshot: business.Snapshot(),
					Err:      settings.ErrUncertain,
				}
				payload = output
			}
		}()
		output.result.Snapshot, output.result.Err = pending.command.apply(business)
		return output, nil
	})
	if err != nil {
		owner.active = nil
		pending.reply.Response(Result{
			Snapshot: owner.view.Clone(),
			Err:      ErrUnavailable,
		})
		owner.dispatch(gc)
	}
}
