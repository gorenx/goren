package session

import (
	"context"

	"github.com/gorenx/goren/agentmessage"
)

// Reader is the detached observation contract offered outside the
// Session owner. Implementations never expose mutable aggregate state.
type Reader interface {
	Header() Header
	ID() SessionID
	FirstLiveSeq() int64
	Seq() int64
	Events() []Event
	Surface() Surface
	Snapshot() Snapshot
	DeriveMessages() ([]agentmessage.Message, error)
}

// Writer is the sole external mutation contract. Every Commit enters the
// same Session-owned FIFO; no raw or optionally serialized path exists.
type Writer interface {
	Commit(context.Context, WritePlan) (WriteResult, error)
}

// Context is the complete capability offered to Session consumers. The
// concrete log and coordinator never escape the session package.
type Context interface {
	Reader
	Writer
}
