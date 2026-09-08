package sessionactor

import (
	"github.com/google/uuid"
	"github.com/gorenx/goren/session"
	goakt "github.com/tochemey/goakt/v4/actor"
)

func (owner *Grain) publishEvents(gc *goakt.GrainContext, events []session.Event) {
	topic := gc.ActorSystem().TopicActor()
	if topic == nil {
		return
	}
	for _, entry := range events {
		fact := CommitNotice{
			SessionID:  owner.history.Header().ID,
			CreationID: owner.creationID,
			Event:      entry,
		}.Clone()
		if err := goakt.Tell(
			gc.Context(),
			topic,
			goakt.NewPublish(uuid.NewString(), session.AppendedEventName, fact),
		); err != nil {
			gc.ActorSystem().Logger().Errorf("session: event publication failed: %v", err)
		}
	}
}
