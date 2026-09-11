package report

// Delivery controls how an accepted report schedules the direct parent Agent.
type Delivery string

const (
	Quiet    Delivery = "quiet"
	NextStep Delivery = "next-step"
)
