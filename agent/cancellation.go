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
