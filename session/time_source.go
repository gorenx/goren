package session

import "time"

// TimeSource supplies event timestamps without coupling log rules to the system clock.
type TimeSource interface {
	CurrentTime() time.Time
}

type systemTimeSource struct{}

func (systemTimeSource) CurrentTime() time.Time {
	return time.Now()
}
