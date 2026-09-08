package llmactor

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/gorenx/goren/llm"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// InitializeWorker supplies LLM-owned routes before the pool is exposed.
type InitializeWorker struct {
	LLM        *goakt.GrainIdentity
	Middleware []llm.StreamMiddleware
}

// Worker multiplexes calls on a reusable mailbox. Each call has at most one
// resource operation in flight; provider I/O never occupies the mailbox.
type Worker struct {
	routes   InitializeWorker
	calls    map[CallID]*invocation
	queries  map[RequestID]*metadataTask
	lifetime context.Context
	stop     context.CancelFunc
}

type invocation struct {
	call      *llm.Call
	operation context.Context
	cancel    context.CancelCauseFunc
	task      *operationTask
	closing   *Delivery
}

type operationTask struct {
	delivery Delivery
	done     chan struct{}
	result   operationResult
}

type operationResult struct {
	call    *llm.Call
	message any
	closed  bool
}

type resultDelivered struct{ err error }

type operationCompleted struct {
	id   CallID
	task *operationTask
}

// PreStart creates only mailbox-owned routing state.
func (worker *Worker) PreStart(ctx *goakt.Context) error {
	worker.lifetime, worker.stop = context.WithCancel(context.WithoutCancel(ctx.Context()))
	worker.calls = make(map[CallID]*invocation)
	worker.queries = make(map[RequestID]*metadataTask)
	return nil
}

// PostStop cancels tasks, waits for their resource ownership to return, then
// attempts cleanup. This is not an acknowledgement to a waiting business owner.
func (worker *Worker) PostStop(ctx *goakt.Context) error {
	worker.stop()
	for _, current := range worker.calls {
		current.cancel(errors.New("llm: worker stopped"))
	}
	var failures []error
	for _, current := range worker.calls {
		retained := current.call
		if current.task != nil {
			<-current.task.done
			if current.task.result.call != nil {
				retained = current.task.result.call
			}
		}
		failures = append(failures, retained.Close(context.WithoutCancel(ctx.Context())))
	}
	return errors.Join(failures...)
}

// Receive handles transport orchestration; Call owns provider behavior.
func (worker *Worker) Receive(rc *goakt.ReceiveContext) {
	switch input := rc.Message().(type) {
	case InitializeWorker:
		var err error
		if worker.routes.LLM != nil || input.LLM == nil {
			err = errors.New("llm: worker requires one initialization with an LLM route")
		} else {
			input.Middleware = slices.Clone(input.Middleware)
			worker.routes = input
		}
		rc.Response(Completion{
			Err: err,
		})
	case ListModels:
		worker.query(rc, input.Query, input)
	case ResolveModel:
		worker.query(rc, input.Query, input)
	case ResolveConfig:
		worker.query(rc, input.Query, input)
	case CancelQuery:
		worker.cancelQuery(input)
	case queryCompleted:
		worker.queryResult(rc, input)
	case Generation:
		worker.generate(rc, input)
	case ReadChunk:
		worker.next(rc, input)
	case CancelCall:
		worker.cancel(rc, input)
	case CloseCall:
		worker.close(rc, input.Delivery)
	case operationCompleted:
		worker.completed(rc, input)
	case resultDelivered:
		if input.err != nil {
			rc.Err(input.err)
		}
	default:
		rc.Unhandled()
	}
}

func validateDelivery(route Delivery) error {
	if route.CallID == "" || route.RequestID == "" || route.ReplyTo == nil {
		return errors.New("llm: operation requires call identity, request identity and reply Grain")
	}
	return nil
}

func (*Worker) reply(rc *goakt.ReceiveContext, recipient *goakt.GrainIdentity, message any) {
	if recipient == nil {
		rc.Err(errors.New("llm: result has no receiving Grain"))
		return
	}
	system := rc.ActorSystem()
	ctx := context.WithoutCancel(rc.Context())
	rc.PipeTo(rc.Self(), func() (any, error) {
		return resultDelivered{
			err: system.TellGrain(ctx, recipient, message),
		}, nil
	})
}

func (worker *Worker) run(rc *goakt.ReceiveContext, current *invocation, route Delivery, execute func() operationResult) {
	task := &operationTask{
		delivery: route,
		done:     make(chan struct{}),
	}
	current.task = task
	// Completion always has a value, including provider failures. PipeTo errors
	// must not replace a typed business result with an unhandled task failure.
	if err := rc.Self().PipeTo(context.WithoutCancel(rc.Context()), rc.Self(), func() (any, error) {
		task.result = execute()
		close(task.done)
		return operationCompleted{
			id:   route.CallID,
			task: task,
		}, nil
	}); err != nil {
		current.task = nil
		close(task.done)
		rc.Err(err)
	}
}

func (worker *Worker) generate(rc *goakt.ReceiveContext, input Generation) {
	failure := validateDelivery(input.Delivery)
	if failure == nil {
		switch settings := input.Settings.(type) {
		case AgentConfig:
			if strings.TrimSpace(settings.AgentID) == "" {
				failure = errors.New("llm: generation requires an Agent identity")
			}
		case Parameters:
			settings = Parameters(llm.CloneCallConfig(llm.CallConfig(settings)))
			input.Settings = settings
			failure = llm.ValidateCallConfig(llm.CallConfig(settings))
		default:
			failure = errors.New("llm: generation requires a configuration choice")
		}
	}
	if failure == nil && worker.routes.LLM == nil {
		failure = errors.New("llm: worker is not initialized")
	}
	if failure == nil && worker.calls[input.CallID] != nil {
		failure = errors.New("llm: generation identity is already active")
	}
	if failure != nil {
		worker.reply(rc, input.ReplyTo, GenerationStarted{
			CallID:    input.CallID,
			RequestID: input.RequestID,
			Err:       failure,
		})
		return
	}
	operation, cancel := context.WithCancelCause(worker.lifetime)
	current := &invocation{
		operation: operation,
		cancel:    cancel,
	}
	worker.calls[input.CallID] = current
	system := rc.ActorSystem()
	routes := worker.routes
	worker.run(rc, current, input.Delivery, func() operationResult {
		result := GenerationStarted{
			CallID:    input.CallID,
			RequestID: input.RequestID,
		}
		prepared, err := prepareGeneration(operation, system, routes, input)
		if prepared != nil {
			result.Config = prepared.ConfigValue()
			result.Defaults = prepared.AdapterDefaultsValue()
			result.RetryPolicy = prepared.RetryPolicyValue()
			if capacity, found := prepared.ContextValue(); found {
				result.Context = &capacity
			}
		}
		result.Err = err
		return operationResult{
			call:    prepared,
			message: result,
		}
	})
}

func prepareGeneration(ctx context.Context, system goakt.ActorSystem, routes InitializeWorker, input Generation) (*llm.Call, error) {
	var snapshot CallSnapshot
	switch settings := input.Settings.(type) {
	case AgentConfig:
		payload, err := system.AskGrain(ctx, routes.LLM, ReadCall{
			AgentID: settings.AgentID,
		}, time.Minute)
		if err != nil {
			return nil, err
		}
		result, ok := payload.(CallSnapshot)
		if !ok {
			return nil, errors.New("llm: invalid call snapshot")
		}
		if result.Err != nil {
			return nil, result.Err
		}
		snapshot = result
	case Parameters:
		explicit := llm.CallConfig(settings)
		payload, err := system.AskGrain(ctx, routes.LLM, Select{
			Provider: explicit.Provider,
		}, time.Minute)
		if err != nil {
			return nil, err
		}
		result, ok := payload.(Selected)
		if !ok {
			return nil, errors.New("llm: invalid binding reply")
		}
		if result.Err != nil {
			var failure *llm.LlmError
			if !errors.As(result.Err, &failure) || failure.Code() != "NO_ADAPTER" {
				return nil, result.Err
			}
		}
		snapshot.Config = explicit
		snapshot.Binding = result.Binding
	default:
		return nil, errors.New("llm: generation requires a configuration choice")
	}
	prepared, err := llm.NewCall(ctx, snapshot.Binding, snapshot.Config, routes.Middleware)
	if err != nil {
		return prepared, err
	}
	options, err := input.Input.Options(prepared.ConfigValue())
	if err != nil {
		return prepared, err
	}
	return prepared, prepared.Open(ctx, options)
}

func (worker *Worker) next(rc *goakt.ReceiveContext, input ReadChunk) {
	current := worker.calls[input.CallID]
	failure := validateDelivery(input.Delivery)
	if failure == nil && (current == nil || current.call == nil) {
		failure = errors.New("llm: generation is not available")
	}
	if failure == nil && (current.task != nil || current.closing != nil) {
		failure = errors.New("llm: generation already has an operation in flight")
	}
	if failure != nil {
		worker.reply(rc, input.ReplyTo, ChunkRead{
			CallID:    input.CallID,
			RequestID: input.RequestID,
			Err:       failure,
		})
		return
	}
	retained, operation := current.call, current.operation
	worker.run(rc, current, input.Delivery, func() operationResult {
		chunk, found, err := retained.Next(operation)
		return operationResult{
			message: ChunkRead{
				CallID:    input.CallID,
				RequestID: input.RequestID,
				Chunk:     chunk,
				Found:     found,
				Err:       err,
			},
		}
	})
}

func (worker *Worker) cancel(rc *goakt.ReceiveContext, input CancelCall) {
	failure := validateDelivery(input.Delivery)
	current := worker.calls[input.CallID]
	if failure == nil && current == nil {
		failure = errors.New("llm: generation is not available")
	}
	if failure == nil {
		current.cancel(input.Cause)
	}
	worker.reply(rc, input.ReplyTo, CancellationAccepted{
		CallID:    input.CallID,
		RequestID: input.RequestID,
		Err:       failure,
	})
}

func (worker *Worker) close(rc *goakt.ReceiveContext, route Delivery) {
	failure := validateDelivery(route)
	current := worker.calls[route.CallID]
	if failure == nil && current == nil {
		failure = errors.New("llm: generation is not available")
	}
	if failure == nil && current.closing != nil {
		failure = errors.New("llm: generation closure is already pending")
	}
	if failure != nil {
		worker.reply(rc, route.ReplyTo, CallClosed{
			CallID:    route.CallID,
			RequestID: route.RequestID,
			Err:       failure,
		})
		return
	}
	if current.task != nil {
		current.cancel(errors.New("llm: generation closing"))
		current.closing = &route
		return
	}
	current.closing = &route
	worker.release(rc, current, route)
}

func (worker *Worker) release(rc *goakt.ReceiveContext, current *invocation, route Delivery) {
	retained := current.call
	cleanup := context.WithoutCancel(current.operation)
	worker.run(rc, current, route, func() operationResult {
		err := retained.Close(cleanup)
		return operationResult{
			closed: err == nil,
			message: CallClosed{
				CallID:    route.CallID,
				RequestID: route.RequestID,
				Err:       err,
			},
		}
	})
}

func (worker *Worker) completed(rc *goakt.ReceiveContext, input operationCompleted) {
	current := worker.calls[input.id]
	if current == nil || current.task != input.task {
		return
	}
	result := input.task.result
	current.task = nil
	if result.call != nil {
		current.call = result.call
	}
	worker.reply(rc, input.task.delivery.ReplyTo, result.message)
	if _, closing := result.message.(CallClosed); closing {
		current.closing = nil
		if result.closed {
			current.cancel(nil)
			delete(worker.calls, input.id)
		}
		return
	}
	if current.closing != nil {
		worker.release(rc, current, *current.closing)
	}
}
