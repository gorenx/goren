package queryactor

import (
	"github.com/gorenx/goren/session/query"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Actor serializes one observed corpus and its index operations. Index lifecycle
// belongs to composition; callers use RequestActor rather than direct access.
type Actor struct {
	service *query.Service
}

// New validates the query owner without opening or acquiring storage.
func New(backend query.Index, settings query.Config) (*Actor, error) {
	owner, err := query.New(backend, settings)
	if err != nil {
		return nil, err
	}
	return &Actor{
		service: owner,
	}, nil
}

// PreStart performs no I/O because composition supplies an open Index.
func (*Actor) PreStart(*goakt.Context) error { return nil }

// PostStop leaves shared storage shutdown to composition.
func (*Actor) PostStop(*goakt.Context) error { return nil }

// Receive dispatches framework messages to the single-owner query business object.
func (owner *Actor) Receive(rc *goakt.ReceiveContext) {
	switch input := rc.Message().(type) {
	case Observe:
		rc.Response(Completion{
			Err: owner.service.Observe(input.Sources),
		})
	case ListSessions:
		items, err := owner.service.ListSessions(rc.Context())
		rc.Response(Sessions{
			Items: items,
			Err:   err,
		})
	case FilterSessions:
		items, err := owner.service.FilterSessions(rc.Context(), input.Constraints)
		rc.Response(Sessions{
			Items: items,
			Err:   err,
		})
	case ReadSession:
		value, err := owner.service.ReadSession(rc.Context(), input.SessionID)
		rc.Response(Log{
			Value: value,
			Err:   err,
		})
	case ReadTitle:
		value, err := owner.service.ReadTitle(rc.Context(), input.SessionID)
		rc.Response(Title{
			Value: value,
			Err:   err,
		})
	case ReadTitles:
		items, err := owner.service.ReadTitleSnapshots(rc.Context(), input.SessionIDs)
		rc.Response(Titles{
			Items: items,
			Err:   err,
		})
	case ListEvents:
		items, err := owner.service.ListEvents(rc.Context(), input.SessionID)
		rc.Response(Events{
			Items: items,
			Err:   err,
		})
	case FilterEvents:
		items, err := owner.service.FilterEvents(rc.Context(), input.SessionID, input.Constraints, input.Text)
		rc.Response(Documents{
			Items: items,
			Err:   err,
		})
	case ReadSurface:
		value, err := owner.service.ReadSurface(rc.Context(), input.SessionID)
		rc.Response(Surface{
			Value: value,
			Err:   err,
		})
	case ReadEvent:
		value, err := owner.service.ReadEvent(rc.Context(), input.Request)
		rc.Response(Event{
			Value: value,
			Err:   err,
		})
	case TraceSession:
		value, err := owner.service.TraceSession(rc.Context(), input.SessionID)
		rc.Response(Lineage{
			Value: value,
			Err:   err,
		})
	case TraceEvent:
		value, err := owner.service.TraceEvent(rc.Context(), input.Request)
		rc.Response(Trace{
			Value: value,
			Err:   err,
		})
	case SearchSessions:
		page, err := owner.service.SearchSessions(rc.Context(), input.Request)
		rc.Response(SessionMatches{
			Page: page,
			Err:  err,
		})
	case SearchEvents:
		page, err := owner.service.SearchEvents(rc.Context(), input.Request)
		rc.Response(EventMatches{
			Page: page,
			Err:  err,
		})
	default:
		rc.Unhandled()
	}
}
