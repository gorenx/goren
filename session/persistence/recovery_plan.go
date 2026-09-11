package persistence

import (
	"fmt"

	"github.com/gorenx/goren/session"
)

// Recovery contains detached initialization input and a storage-only repair command.
// It does not execute IO or replay tool effects.
type Recovery struct {
	Options session.CreateOptions
	Repair  LogRepair
}

// Recover validates stored facts and derives the existing interrupted-turn repairs.
func Recover(identifier session.SessionID, stored Log) (Recovery, error) {
	if stored.Header.ID != identifier {
		return Recovery{}, fmt.Errorf("stored identity mismatch: requested %q, header contains %q", identifier, stored.Header.ID)
	}
	if _, err := inspectStored(stored.Header, stored.Events); err != nil {
		return Recovery{}, err
	}
	closers, err := interruptedTurnClosers(stored.Events)
	if err != nil {
		return Recovery{}, err
	}
	balanced := append(snapshotEvents(stored.Events), closers...)
	if _, err := inspectStored(stored.Header, balanced); err != nil {
		return Recovery{}, err
	}
	return Recovery{
		Options: session.CreateOptions{
			Seed:     balanced,
			Metadata: metadataFromHeader(stored.Header),
		},
		Repair: LogRepair{
			Header:        cloneHeader(stored.Header),
			Marker:        stored.Marker,
			ClosingEvents: snapshotEvents(closers),
		},
	}, nil
}
