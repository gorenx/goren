package projectionactor

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	"github.com/gorenx/goren/session/projection"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

func (owner *Grain) appended(gc *goakt.GrainContext, fact sessionactor.CommitNotice) {
	current, err := owner.projection(fact.SessionID, fact.CreationID)
	if err != nil {
		gc.Response(Completion{
			Err: err,
		})
		return
	}
	changes, err := current.Advance(fact.Event)
	if !errors.Is(err, projection.ErrHistoryRequired) {
		if err == nil {
			publishChanges(gc.Context(), gc.ActorSystem(), fact, changes)
		}
		gc.Response(Completion{
			Err: err,
		})
		return
	}
	if err := gc.EnableReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))); err != nil {
		gc.Response(Completion{
			Err: err,
		})
		return
	}
	system := gc.ActorSystem()
	identity, err := goakt.GrainOf[*sessionactor.Grain](gc.Context(), system, string(fact.SessionID), goakt.WithLongLivedGrain())
	if err != nil {
		gc.Response(Completion{
			Err: err,
		})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(Completion{
			Err: errors.New("sessionprojection: history catchup requires an asynchronous request"),
		})
		return
	}
	ctx := context.WithoutCancel(gc.Context())
	gc.RequestGrain(
		identity,
		sessionactor.Read{
			CreationID: fact.CreationID,
			Minimum: &session.WriteBarrier{
				SessionID: fact.SessionID,
				NextSeq:   fact.Event.Seq + 1,
			},
		},
	).Then(
		func(payload any, failure error) {
			if failure == nil {
				view, ok := payload.(sessionactor.View)
				switch {
				case !ok:
					failure = errors.New("sessionprojection: unexpected Session history response")
				case view.Err != nil:
					failure = view.Err
				case view.Snapshot.Barrier.SessionID != fact.SessionID:
					failure = errors.New("sessionprojection: history belongs to another Session")
				default:
					key := creation{
						sessionID: fact.SessionID,
						id:        fact.CreationID,
					}
					if owner.projections[key] != current {
						failure = errors.New("sessionprojection: projection was released during history read")
					} else {
						changes, failure = current.Observe(view.Snapshot.Events, fact.Event.Seq)
					}
					if failure == nil {
						publishChanges(ctx, system, fact, changes)
					}
				}
			}
			reply.Response(Completion{
				Err: failure,
			})
		},
	)
}

func publishChanges(ctx context.Context, system goakt.ActorSystem, fact sessionactor.CommitNotice, changes []projection.Change) {
	topic := system.TopicActor()
	if topic == nil {
		return
	}
	for _, changed := range changes {
		if err := goakt.Tell(ctx, topic, goakt.NewPublish(uuid.NewString(), ChangedTopic, ChangeNotice{
			SessionID:  fact.SessionID,
			CreationID: fact.CreationID,
			Change:     changed,
		})); err != nil {
			system.Logger().Errorf("sessionprojection: publish failed: %v", err)
		}
	}
}
