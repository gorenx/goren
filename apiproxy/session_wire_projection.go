package apiproxy

import (
	"encoding/json"

	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
)

// ProjectSessionProjections copies one domain projection snapshot into its
// wire block without exposing Registry state.
func ProjectSessionProjections(source sessionprojection.Snapshot) *SessionProjectionsBlock {
	values := make(map[string]json.RawMessage, len(source.Values))
	for key, rawValue := range source.Values {
		values[key] = append(json.RawMessage(nil), rawValue...)
	}
	return &SessionProjectionsBlock{AsOfSeq: source.AsOfSeq, Values: values}
}

// ProjectSessionEvent copies one committed Session fact into the wire event
// shape shared by history and live frames.
func ProjectSessionEvent(committed session.Event) (SessionEvent, error) {
	projected := SessionEvent{
		Type: committed.Type, Seq: committed.Seq, Time: committed.Time,
		Data: append(json.RawMessage(nil), committed.Data...), Ignorable: committed.Ignorable,
	}
	if committed.SourceEventSeqs != nil {
		provenance := append([]int64(nil), (*committed.SourceEventSeqs)...)
		projected.SourceEventSeqs = &provenance
	}
	if committed.SurfaceOp != nil {
		encoded, err := json.Marshal(committed.SurfaceOp)
		if err != nil {
			return SessionEvent{}, err
		}
		projected.SurfaceOp = encoded
	}
	return projected, nil
}
