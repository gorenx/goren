package attachment

// ErrorCode preserves the Harness attachment failure vocabulary.
type ErrorCode string

const (
	InvalidAttachmentRef            ErrorCode = "INVALID_ATTACHMENT_REF"
	AttachmentCorrupt               ErrorCode = "ATTACHMENT_CORRUPT"
	AttachmentWriteFailed           ErrorCode = "ATTACHMENT_WRITE_FAILED"
	AttachmentNotFound              ErrorCode = "ATTACHMENT_NOT_FOUND"
	AttachmentReadFailed            ErrorCode = "ATTACHMENT_READ_FAILED"
	AttachmentProjectionUnsupported ErrorCode = "ATTACHMENT_PROJECTION_UNSUPPORTED"
)

// Error separates a safe public message from a technical cause. Adapters expose
// Code and Message, never the cause's potentially sensitive diagnostic text.
type Error struct {
	Code    ErrorCode
	Message string
	Cause   error
}

func (failure *Error) Error() string { return failure.Message }

// Unwrap permits internal diagnosis without embedding the cause in public text.
func (failure *Error) Unwrap() error { return failure.Cause }
