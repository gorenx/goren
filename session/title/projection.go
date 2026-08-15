package title

import (
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
)

type titleProjectionUnit struct{}

func (titleProjectionUnit) Key() string { return ProjectionKey }

func (titleProjectionUnit) StateVersion() int64 { return 1 }

func (titleProjectionUnit) InitialState() (json.RawMessage, error) {
	return json.RawMessage(`null`), nil
}

func (titleProjectionUnit) ApplyState(state json.RawMessage, committed session.Event) (sessionprojection.Transition, error) {
	if committed.Type != TitleEventName {
		return sessionprojection.Transition{State: state}, nil
	}
	var payload EventData
	if err := json.Unmarshal(committed.Data, &payload); err != nil {
		return sessionprojection.Transition{}, err
	}
	rawValue, err := json.Marshal(payload.Title)
	return sessionprojection.Transition{State: rawValue, Changed: true}, err
}

func (titleProjectionUnit) ViewState(state json.RawMessage) (json.RawMessage, error) {
	if string(state) == "null" {
		return state, nil
	}
	var title string
	if err := json.Unmarshal(state, &title); err != nil {
		return nil, err
	}
	if title == "" {
		return nil, errors.New("sessiontitle: title projection contains an empty title")
	}
	return json.Marshal(title)
}
