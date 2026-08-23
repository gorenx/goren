package subagent

// Mode distinguishes the two Subagent execution strategies.
type Mode string

const (
	// ModeOneShot is one disposable foreground delegation with one result.
	ModeOneShot Mode = "one-shot"
	// ModeContinuable is one durable child conversation with resumable turns.
	ModeContinuable Mode = "continuable"
)
