package modelrequest

import (
	"errors"
	"github.com/gorenx/goren/llm"

	"github.com/gorenx/goren/session"
)

type attemptPhase uint8

const (
	proposedAttempt attemptPhase = iota
	readingLog
	committingHeader
	committingContext
	checkingRequest
	openingStream
	readingStream
	committingChunk
	closingStream
	committingMessage
	completedAttempt
)

// Start requests the committed input baseline without performing I/O.
// A repeated call returns a protocol error without altering an active attempt.
func (attempt *Attempt) Start() (Operation, error) {
	if attempt.phase != proposedAttempt {
		return nil, errors.New("agentloop: model attempt already started")
	}
	attempt.phase = readingLog
	return ReadLog{}, nil
}

// Advance interprets one effect result and chooses the next business operation.
// Cancellation is an error value supplied by the I/O boundary, not a Context.
// Protocol errors leave the outstanding operation unchanged; execution failures
// use Failed and eventually appear in Completed.Err after cleanup.
func (attempt *Attempt) Advance(result Result, cancellation error) (Operation, error) {
	if err := attempt.checkResult(result); err != nil {
		return nil, err
	}
	return attempt.advance(result, cancellation), nil
}

func (attempt *Attempt) advance(result Result, cancellation error) Operation {
	if failure, ok := result.(Failed); ok {
		failure.Err = errors.Join(cancellation, failure.Err)
		if attempt.phase == closingStream {
			attempt.failure = errors.Join(attempt.failure, failure.Err)
			if attempt.failure == nil {
				attempt.failure = errors.New("agentloop: stream closure failed without an error")
			}
			return attempt.complete(attempt.failure)
		}
		return attempt.fail(failure.Err)
	}
	var next Operation
	switch attempt.phase {
	case readingLog:
		read := result.(LogRead)
		if err := attempt.load(read.Snapshot); err != nil {
			return attempt.fail(err)
		}
		attempt.baseline = read.Snapshot.Events
		attempt.phase = openingStream
		next = Generate{
			Input: llm.Input{
				Messages:  attempt.options.Messages,
				System:    attempt.options.System,
				Tools:     attempt.options.Tools,
				SessionID: attempt.options.SessionID,
				Purpose:   attempt.options.Purpose,
			},
		}
	case openingStream:
		started := result.(GenerationStarted)
		if started.Err != nil {
			return attempt.fail(started.Err)
		}
		attempt.options.CallConfig = llm.CloneCallConfig(started.Config)
		attempt.defaults = started.Defaults
		attempt.context = started.Context
		boundary, err := attempt.Prepare(attempt.baseline, attempt.first)
		attempt.baseline = nil
		if err != nil {
			return attempt.fail(err)
		}
		attempt.contextDraft = boundary.Context
		if boundary.Header != nil {
			attempt.phase = committingHeader
			next = attempt.commit(*boundary.Header)
		} else {
			next = attempt.afterHeader()
		}

	case committingHeader, committingContext, committingChunk, committingMessage:
		committed := result.(EventsCommitted)
		if err := attempt.confirmCommit(committed.Write); err != nil {
			return attempt.fail(err)
		}
		switch attempt.phase {
		case committingHeader:
			attempt.headerCommitted = true
			next = attempt.afterHeader()
		case committingContext:
			attempt.phase = checkingRequest
			next = ReadLog{}
		case committingChunk:
			if err := attempt.ConfirmChunk(attempt.pendingChunk, committed.Write.Events); err != nil {
				return attempt.fail(err)
			}
			attempt.pendingChunk = nil
			attempt.phase = readingStream
			next = ReadChunk{}
		case committingMessage:
			if err := attempt.ConfirmMessage(committed.Write.Events); err != nil {
				return attempt.fail(err)
			}
			return attempt.complete(cancellation)
		}
	case checkingRequest:
		read := result.(LogRead)
		if err := attempt.checkRequest(read.Snapshot); err != nil {
			return attempt.fail(err)
		}
		attempt.phase = readingStream
		next = ReadChunk{}

	case readingStream:
		read := result.(ChunkRead)
		if !read.Found {
			attempt.phase = closingStream
			next = CloseStream{}
		} else {
			draft, err := attempt.ChunkDraft(read.Chunk)
			if err != nil {
				return attempt.fail(err)
			}
			attempt.pendingChunk = read.Chunk
			attempt.phase = committingChunk
			next = attempt.commit(draft)
		}
	case closingStream:
		attempt.closed = true
		if attempt.failure != nil {
			return attempt.complete(attempt.failure)
		}
		if cancellation != nil {
			return attempt.complete(cancellation)
		}
		draft, err := attempt.MessageDraft()
		if err != nil {
			return attempt.complete(err)
		}
		if draft == nil {
			return attempt.complete(nil)
		}
		attempt.phase = committingMessage
		return attempt.commit(*draft)
	default:
		return attempt.complete(errors.New("agentloop: model attempt has no pending operation"))
	}
	if cancellation != nil {
		// A selected close has not run yet; cancellation must not skip it.
		if _, closing := next.(CloseStream); closing {
			attempt.failure = errors.Join(attempt.failure, cancellation)
			return next
		}
		return attempt.fail(cancellation)
	}
	return next
}

// RetryClose requests only cleanup after a failed resource-close acknowledgement.
func (attempt *Attempt) RetryClose() (Operation, error) {
	if attempt.phase != completedAttempt {
		return nil, errors.New("agentloop: cleanup retry requires a completed attempt")
	}
	if attempt.closed {
		return attempt.complete(attempt.failure), nil
	}
	attempt.phase = closingStream
	return CloseStream{}, nil
}

func (attempt *Attempt) afterHeader() Operation {
	if attempt.contextDraft != nil {
		draft := *attempt.contextDraft
		attempt.contextDraft = nil
		attempt.phase = committingContext
		return attempt.commit(draft)
	}
	attempt.phase = checkingRequest
	return ReadLog{}
}

func (attempt *Attempt) fail(problem error) Operation {
	if problem == nil {
		problem = errors.New("agentloop: model operation failed without an error")
	}
	attempt.failure = errors.Join(attempt.failure, problem)
	if attempt.closed {
		return attempt.complete(attempt.failure)
	}
	attempt.phase = closingStream
	return CloseStream{}
}

func (attempt *Attempt) complete(problem error) Completed {
	attempt.phase = completedAttempt
	attempt.failure = problem
	result := Completed{
		Finish:          attempt.Finish(),
		Err:             problem,
		StreamClosed:    attempt.closed,
		HeaderCommitted: attempt.headerCommitted,
	}
	if problem == nil {
		result.Message = attempt.Message()
	}
	return result
}

func (attempt *Attempt) load(snapshot session.Snapshot) error {
	history, err := attempt.deriveInput(snapshot)
	if err != nil {
		return err
	}
	if err := attempt.checkStep(snapshot.Events); err != nil {
		return err
	}
	attempt.confirmedNext = snapshot.Barrier.NextSeq
	attempt.options.Messages = history
	return nil
}

// commit retains the exact proposal while the adapter performs the requested I/O.
func (attempt *Attempt) commit(draft session.EventDraft) Operation {
	attempt.pendingDraft = &draft
	return CommitEvents{
		Draft: draft,
	}
}

func (attempt *Attempt) confirmCommit(write session.WriteResult) error {
	if attempt.pendingDraft == nil || len(write.Events) != 1 ||
		string(write.Barrier.SessionID) != attempt.options.SessionID ||
		write.FirstSeq < attempt.confirmedNext || write.NextSeq != write.FirstSeq+1 ||
		write.Barrier.NextSeq != write.NextSeq || write.Events[0].Seq != write.FirstSeq {
		return errors.New("agentloop: model commit acknowledgement does not match the pending write")
	}
	if err := attempt.pendingDraft.ValidateEvent(write.Events[0]); err != nil {
		return err
	}
	attempt.confirmedNext = write.NextSeq
	attempt.pendingDraft = nil
	return nil
}
