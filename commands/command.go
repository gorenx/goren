package commands

import (
	"errors"
	"fmt"
	"strings"

	"github.com/gorenx/goren/session"
)

type commandStage uint8

const (
	runPending commandStage = iota + 1
	handlerReady
	donePending
	commandFinished
)

// Command owns one admitted command's durable lifecycle. It performs no I/O;
// its runtime confirms Session writes and invokes the handler after admission.
type Command struct {
	stage       commandStage
	input       Invocation
	name        string
	attachments int
	opening     eventWrite
	closing     eventWrite
	outcome     Result
	failure     error
}

// NewCommand fixes the command identity and detached input before any write.
func NewCommand(input Invocation, definition Definition, options ExecuteOptions) (*Command, error) {
	if input.CommandID == "" ||
		input.SessionID == "" ||
		options.AttachmentCount < 0 ||
		!commandNamePattern.MatchString(definition.Name) {
		return nil, errors.New("commands: valid command identity, Session and definition are required")
	}
	event := Run{
		CommandID: input.CommandID,
		Name:      definition.Name,
		Source: Source{
			Kind: "user",
		},
	}
	if definition.RecordInput == nil || *definition.RecordInput {
		text := input.RawInput
		event.Args = &text
	}
	draft, err := session.NewEventDraft(RunEvent, event)
	if err != nil {
		return nil, err
	}
	return &Command{
		stage: runPending, input: input,
		name:        definition.Name,
		attachments: options.AttachmentCount,
		opening: eventWrite{
			sessionID: input.SessionID,
			commandID: input.CommandID,
			kind:      RunEventName,
			draft:     draft,
		},
	}, nil
}

// RunWrite returns the immutable start proposal, including on a delivery retry.
func (subject *Command) RunWrite() (session.IdempotentWrite, error) {
	if subject.stage != runPending {
		return nil, errors.New("commands: run is not pending")
	}
	return subject.opening, nil
}

// ConfirmRun admits handler execution only after the exact run event was committed.
func (subject *Command) ConfirmRun(written session.WriteResult) error {
	if subject.stage != runPending {
		return errors.New("commands: run is not pending")
	}
	if err := subject.opening.check(written); err != nil {
		return err
	}
	subject.stage = handlerReady
	if subject.failure != nil {
		return subject.Fail(subject.failure)
	}
	if subject.attachments > 0 {
		message := fmt.Sprintf("/%s does not accept image attachments", subject.name)
		return subject.resolve(Result{
			Kind: ResultError,
			Text: &message,
		}, nil)
	}
	return nil
}

// Invocation returns input only while the handler is allowed to begin.
func (subject *Command) Invocation() (Invocation, error) {
	if subject.stage != handlerReady {
		return Invocation{}, errors.New("commands: handler is not admitted")
	}
	return subject.input, nil
}

// Resolve validates and fixes the first successful handler conclusion.
func (subject *Command) Resolve(outcome Result, snapshot session.Snapshot) error {
	if subject.stage != handlerReady {
		return errors.New("commands: command already has a conclusion or is not admitted")
	}
	if err := validateResult(outcome); err != nil {
		return err
	}
	if snapshot.Barrier.SessionID != subject.input.SessionID {
		return errors.New("commands: result snapshot belongs to another Session")
	}
	if err := validateSourceEvent(snapshot.Events, outcome); err != nil {
		return err
	}
	return subject.resolve(outcome, nil)
}

// Fail fixes a handler failure or cancellation. Later completion cannot replace it.
func (subject *Command) Fail(problem error) error {
	if subject.stage != handlerReady {
		return errors.New("commands: command already has a conclusion or is not admitted")
	}
	if problem == nil {
		return errors.New("commands: failure cause is required")
	}
	text := problem.Error()
	if strings.TrimSpace(text) == "" {
		text = "Command failed."
	}
	return subject.resolve(Result{
		Kind: ResultError,
		Text: &text,
	}, problem)
}

func (subject *Command) resolve(outcome Result, problem error) error {
	draft, err := session.NewEventDraft(DoneEvent, Done{
		CommandID:      subject.input.CommandID,
		Kind:           outcome.Kind,
		Text:           cloneString(outcome.Text),
		SourceEventSeq: cloneInt64(outcome.SourceEventSeq),
	})
	if err != nil {
		return err
	}
	subject.outcome = cloneResult(outcome)
	subject.failure = problem
	subject.closing = eventWrite{
		sessionID: subject.input.SessionID,
		commandID: subject.input.CommandID,
		kind:      DoneEventName,
		draft:     draft,
	}
	subject.stage = donePending
	return nil
}

// DoneWrite returns the fixed conclusion for an idempotent Session commit.
func (subject *Command) DoneWrite() (session.IdempotentWrite, error) {
	if subject.stage != donePending {
		return nil, errors.New("commands: done is not pending")
	}
	return subject.closing, nil
}

// ConfirmDone finishes only the exact pending conclusion.
func (subject *Command) ConfirmDone(written session.WriteResult) error {
	if subject.stage != donePending {
		return errors.New("commands: done is not pending")
	}
	if err := subject.closing.check(written); err != nil {
		return err
	}
	subject.stage = commandFinished
	return nil
}

// Result returns a detached committed result and the original failure, if any.
func (subject *Command) Result() (Execution, error) {
	if subject.stage != commandFinished {
		return Execution{}, errors.New("commands: result is not committed")
	}
	return Execution{
		CommandID: subject.input.CommandID,
		Result:    cloneResult(subject.outcome),
	}, subject.failure
}

// Cancel records intent while run acknowledgement is pending, or fixes the
// admitted command's cancellation. It never fabricates a commit acknowledgement.
func (subject *Command) Cancel(problem error) error {
	if problem == nil {
		return errors.New("commands: cancellation cause is required")
	}
	if subject.stage == runPending {
		if subject.failure == nil {
			subject.failure = problem
		}
		return nil
	}
	return subject.Fail(problem)
}
