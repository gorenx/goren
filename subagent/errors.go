package subagent

// ErrorCode is the stable failure classification vocabulary.
type ErrorCode string

const (
	// ErrorDuplicateSeedBuilder rejects a second live SeedBuilder with the same
	// canonical registry name. The value preserves the compatibility code.
	ErrorDuplicateSeedBuilder ErrorCode = "DUPLICATE_PROVIDER"
	// ErrorNoSeedBuilder reports an unknown SeedBuilder registry name. The value
	// preserves the compatibility code.
	ErrorNoSeedBuilder ErrorCode = "NO_PROVIDER"
	// ErrorDuplicateChild rejects an already owned durable child identity.
	ErrorDuplicateChild ErrorCode = "DUPLICATE_CHILD"
	// ErrorParentUnavailable reports an absent direct parent required for delivery.
	ErrorParentUnavailable ErrorCode = "PARENT_UNAVAILABLE"
	// ErrorUnauthorized rejects a stale or unrelated authority identity.
	ErrorUnauthorized ErrorCode = "UNAUTHORIZED"
	// ErrorNotResumable reports a child that cannot be reconstructed safely.
	ErrorNotResumable ErrorCode = "NOT_RESUMABLE"
	// ErrorDraining rejects admission after a module or parent close cutoff.
	ErrorDraining ErrorCode = "DRAINING"
	// ErrorExtensionRevoked rejects provisioning invalidated by
	// concurrent Extension removal. Its value preserves the pinned DSH code.
	ErrorExtensionRevoked ErrorCode = "ACTIVATION_SETUP_REVOKED"
	// ErrorExtensionReleaseFailed reports contained Extension
	// disposal failures. Its value preserves the pinned DSH code.
	ErrorExtensionReleaseFailed ErrorCode = "ACTIVATION_SETUP_RELEASE_FAILED"
	// ErrorUnknownExtension rejects a selected Extension name that has no live
	// registration in the Subagent Extension Registry.
	ErrorUnknownExtension ErrorCode = "UNKNOWN_SUBAGENT_EXTENSION"
	// ErrorBoundBindingNotFound reports that the exact parent Session has no
	// binding for the requested Bound child.
	ErrorBoundBindingNotFound ErrorCode = "BOUND_BINDING_NOT_FOUND"
	// ErrorBoundDisabled rejects explicit delivery while the global Definition
	// governing the requested Binding is disabled.
	ErrorBoundDisabled ErrorCode = "BOUND_DISABLED"
	// ErrorCancelled reports caller cancellation around listing reads.
	ErrorCancelled ErrorCode = "CANCELLED"
	// ErrorControlProjectionsUnavailable reports a missing projection registry.
	ErrorControlProjectionsUnavailable ErrorCode = "SUBAGENT_CONTROL_PROJECTIONS_UNAVAILABLE"
	// ErrorControlSessionStoreUnavailable reports a missing live Session store.
	ErrorControlSessionStoreUnavailable ErrorCode = "SUBAGENT_CONTROL_SESSION_STORE_UNAVAILABLE"
)

// Error is one typed Subagent failure with an optional causal chain.
type Error struct {
	Code    ErrorCode
	Message string
	Cause   error
}

// Error returns the safe public failure text.
func (problem *Error) Error() string {
	if problem == nil {
		return "subagent: <nil error>"
	}
	return problem.Message
}

// Unwrap exposes the technical cause without changing Code.
func (problem *Error) Unwrap() error {
	if problem == nil {
		return nil
	}
	return problem.Cause
}
