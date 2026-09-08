package agent

// CancelCause is the stable caller intent attached to one active operation.
type CancelCause interface {
	CancelKind() string
}

// UserCancel identifies an explicit caller cancellation.
type UserCancel struct{}

func (UserCancel) CancelKind() string { return "user" }

// ParentCancel identifies cancellation inherited from a parent Agent.
type ParentCancel struct{}

func (ParentCancel) CancelKind() string { return "parent" }

// DisposedCancel identifies structural Agent teardown.
type DisposedCancel struct{}

func (DisposedCancel) CancelKind() string { return "disposed" }

// HookCancel carries one extension-owned cancellation reason.
type HookCancel struct {
	Reason string
}

func (HookCancel) CancelKind() string { return "hook" }

// CancelOptions controls whether pending inbox work survives cancellation.
type CancelOptions struct {
	KeepInbox bool
}

// Cancellation preserves the caller's typed intent as an operation error.
// The error text is diagnostic; consumers inspect Cause for business decisions.
type Cancellation struct {
	Cause CancelCause
}

// NewCancellation supplies the canonical user intent when none was provided.
func NewCancellation(intent CancelCause) Cancellation {
	if intent == nil {
		intent = UserCancel{}
	}
	return Cancellation{
		Cause: intent,
	}
}

func (problem Cancellation) Error() string {
	if problem.Cause == nil {
		return "agent canceled"
	}
	return "agent canceled: " + problem.Cause.CancelKind()
}
