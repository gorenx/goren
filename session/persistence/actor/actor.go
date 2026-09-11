// Package persistenceactor exposes validated durable reads through actor requests.
package persistenceactor

import (
	"github.com/gorenx/goren/session/persistence"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Actor serializes durable reads without owning the shared storage lifetime.
type Actor struct {
	reader *persistence.Reader
}

// New installs a storage port without opening it or reading persisted data.
func New(backend persistence.ReadStorage) (*Actor, error) {
	owner, err := persistence.NewReader(backend)
	if err != nil {
		return nil, err
	}
	return &Actor{
		reader: owner,
	}, nil
}

// PreStart leaves storage initialization to composition.
func (*Actor) PreStart(*goakt.Context) error { return nil }

// PostStop leaves shared storage shutdown to composition.
func (*Actor) PostStop(*goakt.Context) error { return nil }

// Receive delegates read validation to the business Reader.
func (owner *Actor) Receive(rc *goakt.ReceiveContext) {
	switch input := rc.Message().(type) {
	case Inspect:
		value, err := owner.reader.Inspect(rc.Context(), input.SessionID)
		rc.Response(Inspection{
			Value: value,
			Err:   err,
		})
	case ListSnapshots:
		value, err := owner.reader.ListSnapshots(rc.Context(), input.Page)
		rc.Response(Snapshots{
			Value: value,
			Err:   err,
		})
	case ReadEventsFrom:
		value, err := owner.reader.ReadEventsFrom(rc.Context(), input.SessionID, input.Page)
		rc.Response(Segment{
			Value: value,
			Err:   err,
		})
	case ReadEventsBefore:
		value, err := owner.reader.ReadEventsBefore(rc.Context(), input.SessionID, input.Page)
		rc.Response(Window{
			Value: value,
			Err:   err,
		})
	default:
		rc.Unhandled()
	}
}
