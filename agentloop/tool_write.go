package agentloop

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
)

// toolWrite identifies one immutable call, result, or cancellation pair by the
// model call and Step. Session resolves retries before appending another event.
type toolWrite struct {
	sessionID session.SessionID
	position  session.StepPosition
	callID    agentmessage.CallID
	eventType string
	drafts    []session.EventDraft
}

func (plan toolWrite) Build(_ context.Context, snapshot session.Snapshot) ([]session.EventDraft, error) {
	if snapshot.Barrier.SessionID != plan.sessionID {
		return nil, errors.New("agentloop: tool write belongs to another Session")
	}
	return append([]session.EventDraft(nil), plan.drafts...), nil
}

func (plan toolWrite) FindCommit(snapshot session.Snapshot) (*session.WriteResult, error) {
	if snapshot.Barrier.SessionID != plan.sessionID || snapshot.Barrier.NextSeq != int64(len(snapshot.Events)) {
		return nil, errors.New("agentloop: tool write requires its complete Session snapshot")
	}
	var found *session.WriteResult
	for index, entry := range snapshot.Events {
		if entry.Seq != int64(index) {
			return nil, errors.New("agentloop: tool snapshot sequence is discontinuous")
		}
		if entry.Type != plan.eventType {
			continue
		}
		var position session.StepPosition
		err := json.Unmarshal(entry.Data, &position)
		if err != nil {
			return nil, err
		}
		if position != plan.position {
			continue
		}
		var identifier agentmessage.CallID
		if entry.Type == session.ToolCallEventName {
			var call session.ToolCall
			err = json.Unmarshal(entry.Data, &call)
			identifier = call.CallID
		} else {
			message, failure := session.DeriveEventMessage(entry)
			if failure != nil {
				return nil, failure
			}
			if message == nil {
				return nil, errors.New("agentloop: tool result message is absent")
			}
			for _, block := range message.ContentValue() {
				if outcome, ok := block.(agentmessage.ToolResultBlock); ok {
					identifier = outcome.ToolCallID
				}
			}
		}
		if err != nil {
			return nil, err
		}
		if identifier != plan.callID {
			continue
		}
		if found != nil || index+len(plan.drafts) > len(snapshot.Events) {
			return nil, errors.New("agentloop: duplicate or incomplete tool commit")
		}
		for offset, draft := range plan.drafts {
			err = draft.ValidateEvent(snapshot.Events[index+offset])
			if err != nil {
				return nil, err
			}
		}
		next := entry.Seq + int64(len(plan.drafts))
		found = &session.WriteResult{
			FirstSeq: entry.Seq,
			NextSeq:  next,
			Barrier: session.WriteBarrier{
				SessionID: plan.sessionID,
				NextSeq:   next,
			},
			Events: snapshot.Events[index : index+len(plan.drafts)],
		}
	}
	return found, nil
}

func (plan toolWrite) commit(services Services, receiver CommitReceiver) {
	err := services.Conversation.Commit(plan, receiver)
	if err != nil {
		receiver.SessionCommitted(services, session.WriteResult{}, err)
	}
}

// contextWrite keeps the authored message identity and original Inbox insertion
// across an uncertain acknowledgement. It never prepares a second insertion.
type contextWrite struct {
	sessionID session.SessionID
	messageID agentmessage.MessageID
	drafts    []session.EventDraft
}

func (plan contextWrite) Build(_ context.Context, snapshot session.Snapshot) ([]session.EventDraft, error) {
	if snapshot.Barrier.SessionID != plan.sessionID {
		return nil, errors.New("agentloop: context belongs to another Session")
	}
	return append([]session.EventDraft(nil), plan.drafts...), nil
}

func (plan contextWrite) FindCommit(snapshot session.Snapshot) (*session.WriteResult, error) {
	if snapshot.Barrier.SessionID != plan.sessionID || snapshot.Barrier.NextSeq != int64(len(snapshot.Events)) {
		return nil, errors.New("agentloop: context requires its complete Session snapshot")
	}
	var found *session.WriteResult
	for index, entry := range snapshot.Events {
		if entry.Seq != int64(index) {
			return nil, errors.New("agentloop: context snapshot sequence is discontinuous")
		}
		if entry.Type != agent.InboxSplicedEventName {
			continue
		}
		var splice agent.InboxSplice
		err := json.Unmarshal(entry.Data, &splice)
		if err != nil {
			return nil, err
		}
		for _, message := range splice.Inserted {
			if message.StableID() != plan.messageID {
				continue
			}
			if found != nil || index+len(plan.drafts) > len(snapshot.Events) {
				return nil, errors.New("agentloop: duplicate or incomplete context insertion")
			}
			for offset, draft := range plan.drafts {
				err = draft.ValidateEvent(snapshot.Events[index+offset])
				if err != nil {
					return nil, err
				}
			}
			next := entry.Seq + int64(len(plan.drafts))
			found = &session.WriteResult{
				FirstSeq: entry.Seq,
				NextSeq:  next,
				Barrier: session.WriteBarrier{
					SessionID: plan.sessionID,
					NextSeq:   next,
				},
				Events: snapshot.Events[index : index+len(plan.drafts)],
			}
		}
	}
	return found, nil
}
