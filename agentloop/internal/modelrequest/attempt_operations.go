package modelrequest

import (
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

// Operation is a closed set of effects requested by one model attempt.
// Implementations describe business intent, never actor addresses or callbacks.
type Operation interface{ modelOperation() }

type ReadLog struct{}
type CommitEvents struct{ Draft session.EventDraft }
type OpenStream struct{ Options llm.GenerateOptions }
type ReadChunk struct{}
type CloseStream struct{}

// Completed separates business failure from confirmed resource release.
type Completed struct {
	Message         agentmessage.AssistantMessage
	Finish          llm.FinishReason
	Err             error
	StreamClosed    bool
	HeaderCommitted bool
}

func (ReadLog) modelOperation()      {}
func (CommitEvents) modelOperation() {}
func (OpenStream) modelOperation()   {}
func (ReadChunk) modelOperation()    {}
func (CloseStream) modelOperation()  {}
func (Completed) modelOperation()    {}

// Result contains the outcome of exactly one requested operation.
type Result interface{ modelResult() }
type LogRead struct{ Snapshot session.Snapshot }

// EventsCommitted carries the complete acknowledgement, including Session identity.
type EventsCommitted struct{ Write session.WriteResult }
type StreamOpened struct{}
type ChunkRead struct {
	Chunk llm.StreamChunk
	Found bool
}
type StreamClosed struct{}
type Failed struct{ Err error }

func (LogRead) modelResult()         {}
func (EventsCommitted) modelResult() {}
func (StreamOpened) modelResult()    {}
func (ChunkRead) modelResult()       {}
func (StreamClosed) modelResult()    {}
func (Failed) modelResult()          {}
