package subagent

// ErrorCode is the stable failure classification vocabulary.
type ErrorCode string

const (
	// ErrorDuplicateProvider rejects a second live Provider with the same name.
	ErrorDuplicateProvider ErrorCode = "DUPLICATE_PROVIDER"
	// ErrorNoProvider reports an unknown Provider name.
	ErrorNoProvider ErrorCode = "NO_PROVIDER"
	// ErrorUnsupportedCapability rejects an input the selected Provider lacks.
	ErrorUnsupportedCapability ErrorCode = "UNSUPPORTED_CAPABILITY"
	// ErrorContinuationUnavailable reports a missing continuation manager.
	ErrorContinuationUnavailable ErrorCode = "CONTINUATION_UNAVAILABLE"
	// ErrorDuplicateChild rejects an already owned durable child identity.
	ErrorDuplicateChild ErrorCode = "DUPLICATE_CHILD"
	// ErrorPersistenceUnavailable reports missing durability for cold resume.
	ErrorPersistenceUnavailable ErrorCode = "PERSISTENCE_UNAVAILABLE"
	// ErrorParentUnavailable reports an absent direct parent required for delivery.
	ErrorParentUnavailable ErrorCode = "PARENT_UNAVAILABLE"
	// ErrorUnauthorized rejects a stale or unrelated authority identity.
	ErrorUnauthorized ErrorCode = "UNAUTHORIZED"
	// ErrorNotResumable reports a child that cannot be reconstructed safely.
	ErrorNotResumable ErrorCode = "NOT_RESUMABLE"
	// ErrorActivationClosing rejects admission into a tearing-down Activation.
	ErrorActivationClosing ErrorCode = "ACTIVATION_CLOSING"
	// ErrorActivationTeardownFailed reports failure to release an Activation.
	ErrorActivationTeardownFailed ErrorCode = "ACTIVATION_TEARDOWN_FAILED"
	// ErrorDraining rejects admission below an exact parent teardown cutoff.
	ErrorDraining ErrorCode = "DRAINING"
	// ErrorActivationExtensionRevoked rejects provisioning invalidated by
	// concurrent Extension removal. Its value preserves the pinned DSH code.
	ErrorActivationExtensionRevoked ErrorCode = "ACTIVATION_SETUP_REVOKED"
	// ErrorActivationExtensionReleaseFailed reports contained Extension
	// disposal failures. Its value preserves the pinned DSH code.
	ErrorActivationExtensionReleaseFailed ErrorCode = "ACTIVATION_SETUP_RELEASE_FAILED"
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
