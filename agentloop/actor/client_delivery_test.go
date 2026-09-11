package loopactor_test

import (
	"context"
	"errors"
	"testing"
	"time"

	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// These probe contracts deliberately contain no framework types. The business
// receiver decides whether to request another value; the Grain only delivers.
type deliveryCapability interface {
	Read(deliveryReceiver) error
}

type deliveryReceiver interface {
	Accept(deliveryCapability, deliveryValue)
}

type deliveryValue struct {
	text string
	err  error
}

type deliveryBusiness struct {
	returned bool
	inputs   int
	results  int
	failure  error
	done     chan deliveryReport
}

type deliveryReport struct {
	inputs  int
	results int
	err     error
}

func (subject *deliveryBusiness) start(endpoint deliveryCapability) {
	subject.failure = endpoint.Read(subject)
	subject.returned = true
}

func (subject *deliveryBusiness) Accept(endpoint deliveryCapability, value deliveryValue) {
	subject.results++
	if !subject.returned {
		subject.failure = errors.New("business result arrived before submission returned")
	}
	subject.failure = errors.Join(subject.failure, value.err)
	if subject.failure == nil && subject.results == 1 {
		subject.failure = endpoint.Read(subject)
		if subject.failure == nil {
			return
		}
	}
	subject.done <- deliveryReport{
		inputs:  subject.inputs,
		results: subject.results,
		err:     subject.failure,
	}
}

type deliveryStart struct {
	target  *goakt.GrainIdentity
	timeout time.Duration
	done    chan deliveryReport
}

type deliveryNotice struct {
	receiver deliveryReceiver
	value    deliveryValue
}

type deliveryInput struct{}
type deliveryInspect struct{}
type deliveryQuery struct{ round int }
type deliveryRelease struct{}
type deliverySetup struct{ entered chan int }

// deliveryPort exists only for the current receive call. Neither the business
// object nor the delayed callback retains this port or its GrainContext.
type deliveryPort struct {
	current *goakt.GrainContext
	target  *goakt.GrainIdentity
	timeout time.Duration
	round   int
}

func (endpoint deliveryPort) Read(receiver deliveryReceiver) error {
	completed := make(chan deliveryNotice, 1)
	err := endpoint.current.PipeToSelf(func() (any, error) {
		return <-completed, nil
	})
	if err != nil {
		return err
	}
	endpoint.current.RequestGrain(endpoint.target, deliveryQuery{round: endpoint.round},
		goakt.WithRequestTimeout(endpoint.timeout),
		goakt.WithReentrancyMode(reentrancy.AllowAll),
	).Then(func(payload any, failure error) {
		value, valid := payload.(string)
		if failure == nil && !valid {
			failure = errors.New("invalid lookup response")
		}
		completed <- deliveryNotice{
			receiver: receiver,
			value: deliveryValue{
				text: value,
				err:  failure,
			},
		}
	})
	return nil
}

type deliveryGrain struct {
	subject deliveryBusiness
	config  deliveryStart
	round   int
}

func (*deliveryGrain) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*deliveryGrain) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }

func (owner *deliveryGrain) OnReceive(gc *goakt.GrainContext) {
	owner.round++
	switch input := gc.Message().(type) {
	case deliveryStart:
		owner.config = input
		owner.subject.done = input.done
		owner.subject.start(owner.port(gc))
		gc.Response(struct{}{})
	case deliveryNotice:
		input.receiver.Accept(owner.port(gc), input.value)
		gc.NoErr()
	case deliveryInput:
		owner.subject.inputs++
		gc.Response(struct{}{})
	case deliveryInspect:
		gc.Response(deliveryReport{
			inputs:  owner.subject.inputs,
			results: owner.subject.results,
			err:     owner.subject.failure,
		})
	default:
		gc.Unhandled()
	}
}

func (owner *deliveryGrain) port(gc *goakt.GrainContext) deliveryPort {
	return deliveryPort{
		current: gc,
		target:  owner.config.target,
		timeout: owner.config.timeout,
		round:   owner.round,
	}
}

type deliveryService struct {
	entered chan int
	first   *goakt.GrainReply
	calls   int
}

func (*deliveryService) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*deliveryService) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }

func (owner *deliveryService) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case deliverySetup:
		owner.entered = input.entered
		gc.Response(struct{}{})
	case deliveryQuery:
		owner.calls++
		owner.entered <- input.round
		if owner.calls == 1 {
			owner.first = gc.DeferResponse()
			return
		}
		gc.Response("ready")
	case deliveryRelease:
		owner.first.Response("ready")
		owner.first = nil
		gc.Response(struct{}{})
	default:
		gc.Unhandled()
	}
}

func TestCapabilityDeliveryUsesFreshTurnWithoutBusinessFlowInActor(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		invalid bool
		timeout time.Duration
	}{
		{name: "delayed-success"},
		{
			name:    "immediate-failure",
			invalid: true,
		},
		{
			name:    "timeout-and-late-reply",
			timeout: 50 * time.Millisecond,
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			system, err := goakt.NewActorSystem("delivery-probe", goakt.WithLogger(log.DiscardLogger))
			if err != nil {
				t.Fatal(err)
			}
			err = system.Start(ctx)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
				defer stop()
				failure := system.Stop(cleanup)
				if failure != nil {
					t.Error(failure)
				}
			})
			endpoint, err := goakt.GrainOf[*deliveryService](ctx, system, "lookup", goakt.WithLongLivedGrain())
			if err != nil {
				t.Fatal(err)
			}
			serviceDriver, serviceReplies := grainDriver(t, system, endpoint)
			entered := make(chan int, 4)
			request[struct{}](t, serviceDriver, serviceReplies, deliverySetup{entered: entered})
			subject, err := goakt.GrainOf[*deliveryGrain](ctx, system, "business", goakt.WithLongLivedGrain(),
				goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
			if err != nil {
				t.Fatal(err)
			}
			driver, replies := grainDriver(t, system, subject)
			target := endpoint
			if scenario.invalid {
				target = nil
			}
			done := make(chan deliveryReport, 4)
			request[struct{}](t, driver, replies, deliveryStart{
				target:  target,
				timeout: scenario.timeout,
				done:    done,
			})
			firstRound := 0
			if !scenario.invalid {
				select {
				case firstRound = <-entered:
				case <-ctx.Done():
					t.Fatal("lookup did not start")
				}
				request[struct{}](t, driver, replies, deliveryInput{})
				if scenario.timeout == 0 {
					request[struct{}](t, serviceDriver, serviceReplies, deliveryRelease{})
				}
			}
			var report deliveryReport
			select {
			case report = <-done:
			case <-ctx.Done():
				t.Fatal("business continuation did not finish")
			}
			if scenario.invalid || scenario.timeout > 0 {
				if report.err == nil || report.results != 1 {
					t.Fatalf("failure delivery: %+v", report)
				}
			} else {
				if report.err != nil || report.results != 2 || report.inputs != 1 {
					t.Fatalf("business continuation: %+v", report)
				}
				select {
				case nextRound := <-entered:
					if nextRound <= firstRound {
						t.Fatal("continuation reused the previous receive context")
					}
				case <-ctx.Done():
					t.Fatal("business did not submit the second lookup")
				}
			}
			if scenario.timeout > 0 {
				request[struct{}](t, serviceDriver, serviceReplies, deliveryRelease{})
				observed := request[deliveryReport](t, driver, replies, deliveryInspect{})
				if observed.results != 1 {
					t.Fatalf("late reply delivered again: %+v", observed)
				}
			}
		})
	}
}
