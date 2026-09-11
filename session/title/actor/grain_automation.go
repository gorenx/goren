package titleactor

import (
	"errors"

	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	"github.com/gorenx/goren/session/title"
	goakt "github.com/tochemey/goakt/v4/actor"
)

func (owner *Grain) observeEvent(gc *goakt.GrainContext, input sessionactor.CommitNotice) {
	if RuntimeName(string(input.SessionID)) != gc.Self().Name() || input.Event.Seq < 0 {
		gc.Response(Updated{
			Err: errors.New("title: invalid Session notification"),
		})
		return
	}
	selected, err := owner.resolve(input.CreationID)
	if err != nil {
		gc.Response(Updated{
			Err: err,
		})
		return
	}
	switch input.Event.Type {
	case title.TitleEventName:
		accepted, err := title.Fold([]session.Event{input.Event})
		if err == nil && accepted != nil && accepted.Source.SourceKind() == "user" {
			selected.cancel()
		}
		gc.Response(Updated{
			Err: err,
		})
	case session.UserMessageEventName:
		messages, err := title.CollectMessages([]session.Event{input.Event}, nil)
		if err != nil || len(messages) == 0 {
			gc.Response(Updated{
				Err: err,
			})
			return
		}
		if selected.provider == nil {
			owner.commit(gc, selected, input.CreationID, selected.rules.Fallback())
			return
		}
		entry := input.Event.Clone()
		owner.beginGeneration(gc, Generate{
			CreationID: input.CreationID,
			Provider:   selected.provider,
		}, &entry)
	default:
		gc.Response(Updated{})
	}
}
