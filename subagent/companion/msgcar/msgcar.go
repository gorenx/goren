// Package msgcar relays live parent conversation messages to a memory Companion.
package msgcar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	agentregistry "github.com/gorenx/goren/agent/registry"
	loopactor "github.com/gorenx/goren/agentloop/actor"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	gerrors "github.com/tochemey/goakt/v4/errors"
	"github.com/tochemey/goakt/v4/reentrancy"
)

const operationTimeout = 30 * time.Second

// Actor owns one exact subscription and serializes ordinary Followup delivery.
type Actor struct {
	parent   agentregistry.Creation
	child    agentregistry.Creation
	executor *goakt.GrainIdentity
	ready    chan struct{}
	lastSeq  int64
	turn     int64
	final    *forward
	queue    []forward
	active   *forward
	activeID agentmessage.MessageID
}

type forward struct {
	speaker string
	text    string
}

type delivered struct {
	message agentmessage.MessageID
	err     error
}

// Start subscribes after the child is Ready and waits for PubSub acknowledgement.
func Start(ctx context.Context, system goakt.ActorSystem, parent, child agentregistry.Creation, executor *goakt.GrainIdentity) error {
	if ctx == nil || system == nil || parent.AgentID == "" || parent.CreationID == "" || child.AgentID == "" || child.CreationID == "" || executor == nil {
		return errors.New("msgcar: exact parent, child, and executor are required")
	}
	listener := &Actor{
		parent:   parent,
		child:    child,
		executor: executor,
		ready:    make(chan struct{}),
		lastSeq:  -1,
	}
	name := RuntimeName(child)
	_, err := system.Spawn(ctx, name, listener,
		goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		return err
	}
	waiting, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	select {
	case <-listener.ready:
		return nil
	case <-waiting.Done():
		return errors.Join(waiting.Err(), Stop(context.Background(), system, child))
	}
}

// RuntimeName addresses only this exact child creation.
func RuntimeName(child agentregistry.Creation) string {
	return fmt.Sprintf("msgcar.%s.%s", child.AgentID, child.CreationID)
}

// Stop releases the named Actor without requiring Steward to retain a PID.
// An absent Actor has no remaining subscription to release.
func Stop(ctx context.Context, system goakt.ActorSystem, child agentregistry.Creation) error {
	if ctx == nil || system == nil || child.AgentID == "" || child.CreationID == "" {
		return errors.New("msgcar: context, ActorSystem and exact child are required")
	}
	err := system.Kill(ctx, RuntimeName(child))
	if errors.Is(err, gerrors.ErrActorNotFound) {
		return nil
	}
	return err
}

func (*Actor) PreStart(*goakt.Context) error { return nil }
func (*Actor) PostStop(*goakt.Context) error { return nil }

// Receive filters the parent creation and holds the last final text until Turn completion.
func (owner *Actor) Receive(rc *goakt.ReceiveContext) {
	switch input := rc.Message().(type) {
	case *goakt.PostStart:
		rc.Tell(rc.ActorSystem().TopicActor(), goakt.NewSubscribe(session.AppendedEventName))
	case *goakt.SubscribeAck:
		if input.Topic() == session.AppendedEventName {
			select {
			case <-owner.ready:
			default:
				close(owner.ready)
			}
		}
	case sessionactor.CommitNotice:
		owner.observe(rc, input)
	case delivered:
		if owner.active == nil || input.message != owner.activeID {
			return
		}
		if input.err != nil {
			rc.ActorSystem().Logger().Errorf("msgcar: Followup delivery failed: %v", input.err)
		}
		owner.active = nil
		owner.activeID = ""
		owner.sendNext(rc)
	default:
		rc.Unhandled()
	}
}

func (owner *Actor) observe(rc *goakt.ReceiveContext, notice sessionactor.CommitNotice) {
	if notice.SessionID != owner.parent.AgentID ||
		notice.CreationID != owner.parent.CreationID ||
		notice.Event.Seq <= owner.lastSeq {
		return
	}
	owner.lastSeq = notice.Event.Seq
	event := notice.Event
	switch event.Type {
	case session.UserMessageEventName:
		message, err := agentmessage.DecodeUserMessage(event.Data)
		if err != nil {
			rc.ActorSystem().Logger().Errorf("msgcar: decode parent user message: %v", err)
			return
		}
		if origin := message.SourceValue(); origin == nil || origin.SourceKind() != "user" {
			return
		}
		if visible := plainText(message.ContentValue()); visible != "" {
			owner.queue = append(owner.queue, forward{
				speaker: "User",
				text:    visible,
			})
			owner.sendNext(rc)
		}
	case session.AssistantMessageEventName:
		var payload struct {
			Turn int64 `json:"turn"`
		}
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			rc.ActorSystem().Logger().Errorf("msgcar: decode assistant position: %v", err)
			return
		}
		message, err := session.DeriveEventMessage(event)
		if err != nil {
			rc.ActorSystem().Logger().Errorf("msgcar: decode assistant message: %v", err)
			return
		}
		if message == nil {
			return
		}
		content := message.ContentValue()
		for _, block := range content {
			if block.ContentType() == "tool-call" {
				owner.final = nil
				return
			}
		}
		if visible := plainText(content); visible != "" {
			owner.turn = payload.Turn
			owner.final = &forward{
				speaker: "Agent",
				text:    visible,
			}
		}
	case session.TurnEndEventName:
		var payload struct {
			Turn   int64 `json:"turn"`
			Reason struct {
				Kind string `json:"kind"`
			} `json:"reason"`
		}
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			rc.ActorSystem().Logger().Errorf("msgcar: decode turn end: %v", err)
			return
		}
		if owner.final != nil && owner.turn == payload.Turn && payload.Reason.Kind == "completed" {
			owner.queue = append(owner.queue, *owner.final)
			owner.sendNext(rc)
		}
		owner.final = nil
	}
}

func plainText(blocks []agentmessage.ContentBlock) string {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if visible, ok := block.(agentmessage.PlainTextContent); ok {
			if value, isText := visible.PlainText(); isText && strings.TrimSpace(value) != "" {
				parts = append(parts, value)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func (owner *Actor) sendNext(rc *goakt.ReceiveContext) {
	if owner.active != nil || len(owner.queue) == 0 {
		return
	}
	next := owner.queue[0]
	owner.queue = owner.queue[1:]
	message, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock(next.speaker + ": " + next.text)},
		Source: agentmessage.PluginMessageSource{
			Plugin: "msgcar",
			Form:   agentmessage.ContextRelay,
		},
	})
	if err != nil {
		rc.ActorSystem().Logger().Errorf("msgcar: construct Followup: %v", err)
		owner.sendNext(rc)
		return
	}
	owner.active = &next
	owner.activeID = message.StableID()
	self := rc.Self()
	logger := rc.ActorSystem().Logger()
	rc.RequestGrain(owner.executor, loopactor.Followup{
		CreationID: owner.child.CreationID,
		Message:    message,
	}, goakt.WithRequestTimeout(0)).Then(func(payload any, failure error) {
		if failure == nil {
			changed, ok := payload.(loopactor.Changed)
			if !ok {
				failure = errors.New("msgcar: invalid Followup acknowledgement")
			} else {
				failure = changed.Err
				if failure == nil && !slices.ContainsFunc(changed.Changes.Inserted, func(entry agentmessage.UserMessage) bool {
					return entry.StableID() == message.StableID()
				}) {
					failure = errors.New("msgcar: Followup did not confirm Inbox admission")
				}
			}
		}
		if err := goakt.Tell(context.Background(), self, delivered{
			message: message.StableID(),
			err:     failure,
		}); err != nil {
			logger.Errorf("msgcar: delivery completion failed: %v", err)
		}
	})
}
