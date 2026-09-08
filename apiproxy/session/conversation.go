package sessionapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	sessionframes "github.com/gorenx/goren/apiproxy/session/events"

	"path/filepath"
	"regexp"
	"slices"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	rpc "github.com/gorenx/goren/apiproxy/rpc"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/llm"
)

var ianaTimeZonePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+.-]*(?:/[A-Za-z0-9_+.-]+)+$`)

type sessionConversation struct {
	access *sessionAccess
	models Models
}

func (flow *sessionConversation) Prompt(
	requestContext context.Context,
	call rpc.Request[PromptRequest],
) (rpc.Outcome[PromptValue], error) {
	subject, refused := flow.access.ordinaryAgent(requestContext, call.Payload.SessionID)
	if refused != nil {
		return rpc.Fail[PromptValue](*refused), nil
	}
	current, err := flow.models.Configuration(requestContext, subject.ID())
	if err != nil {
		return rpc.Outcome[PromptValue]{}, err
	}
	catalog, err := flow.models.ReadCatalog(requestContext)
	if err != nil {
		return rpc.Outcome[PromptValue]{}, err
	}
	if !slices.ContainsFunc(
		catalog.Active,
		func(provider llm.ProviderInfo) bool {
			return provider.ID == current.Provider
		}) {
		return rpc.Fail[PromptValue](
			rpc.NewRPCError(
				connection.ErrorModelUnavailable,
				fmt.Sprintf("no adapter serves provider %q; select a model for this session", current.Provider),
				struct {
					Provider string `json:"provider"`
					Model    string `json:"model"`
				}{
					Provider: current.Provider,
					Model:    current.Model,
				},
			),
		), nil
	}
	canonicalZone := ""
	if call.Payload.ClientTimeZone != nil {
		canonicalZone, err = canonicalClientTimeZone(*call.Payload.ClientTimeZone)
		if err != nil {
			return rpc.Fail[PromptValue](rpc.NewRPCError(
				connection.ErrorInvalidTimeZone,
				"clientTimeZone must be UTC or a valid IANA Area/Location name",
				struct {
					Value string `json:"value"`
				}{
					Value: *call.Payload.ClientTimeZone,
				},
			)), nil
		}
	}
	content := make([]agentmessage.ContentBlock, 0, len(call.Payload.Content))
	for _, part := range call.Payload.Content {
		switch typedPart := part.(type) {
		case PromptTextPart:
			content = append(content, agentmessage.NewTextBlock(typedPart.Text))
		case PromptImagePart:
			return rpc.Fail[PromptValue](rpc.NewRPCError(
				connection.ErrorAttachment,
				"Image input is unavailable: this deployment mounts no attachment service.",
				struct {
					Reason string `json:"reason"`
				}{
					Reason: "ATTACHMENT_UNAVAILABLE",
				},
			)), nil
		}
	}
	originJSON, err := encodePromptSource(call.RPCID, canonicalZone)
	if err != nil {
		return rpc.Outcome[PromptValue]{}, err
	}
	origin, err := agentmessage.NewOpaqueMessageSource("user", originJSON)
	if err != nil {
		return rpc.Outcome[PromptValue]{}, err
	}
	messageValue, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: content,
		Source:  origin,
	})
	if err != nil {
		return rpc.Outcome[PromptValue]{}, err
	}
	if call.Payload.Mode == "steer" {
		err = subject.Steer(requestContext, messageValue)
	} else {
		err = subject.Followup(requestContext, messageValue)
	}
	if err != nil {
		return rpc.Fail[PromptValue](rpc.NewRPCError(
			connection.ErrorAgentBusy, "prompt rejected", struct {
				Reason string `json:"reason"`
			}{
				Reason: err.Error(),
			},
		)), nil
	}
	return rpc.OK(PromptValue{
		Accepted: true,
	}), nil
}

// UpdateQueue edits, removes, or strictly steers one still-pending occurrence.
func (flow *sessionConversation) UpdateQueue(requestContext context.Context, call rpc.Request[UpdateQueueRequest]) (rpc.Outcome[AcceptedValue], error) {
	if action, ok := call.Payload.Action.(EditQueueAction); ok {
		for _, rawBlock := range action.Content {
			var header struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(rawBlock, &header)
			if header.Type != "text" {
				return rpc.Fail[AcceptedValue](rpc.NewRPCError(
					connection.ErrorAttachment, "queue edits accept text content only", struct {
						Reason string `json:"reason"`
					}{
						Reason: "QUEUE_EDIT_NON_TEXT",
					},
				)), nil
			}
		}
	}

	subject, refused := flow.access.attachedAgent(requestContext, call.Payload.SessionID)
	if refused != nil {
		if refused.Code == connection.ErrorSessionNotFound {
			return rpc.Fail[AcceptedValue](queueItemNotFoundError(call.Payload.ItemID)), nil
		}
		return rpc.Fail[AcceptedValue](*refused), nil
	}
	pendingID := agentmessage.MessageID(call.Payload.ItemID)
	pending, err := subject.Pending(requestContext)
	if err != nil {
		return rpc.Outcome[AcceptedValue]{}, err
	}
	messageValue, _, found := locatePending(pending, pendingID)
	if !found {
		return rpc.Fail[AcceptedValue](queueItemNotFoundError(call.Payload.ItemID)), nil
	}
	switch action := call.Payload.Action.(type) {
	case EditQueueAction:
		replacement, err := replaceMessageContent(messageValue, action.Content)
		if err != nil {
			return rpc.Outcome[AcceptedValue]{}, err
		}
		if err := subject.Replace(requestContext, pendingID, replacement); err != nil {
			return queueFailure(call.Payload.ItemID, err)
		}
	case RemoveQueueAction:
		if err := subject.Remove(requestContext, pendingID); err != nil {
			return queueFailure(call.Payload.ItemID, err)
		}
	case SteerQueueAction:
		if err := subject.Promote(requestContext, pendingID); err != nil {
			return queueFailure(call.Payload.ItemID, err)
		}

	}
	return rpc.OK(AcceptedValue{
		Accepted: true,
	}), nil
}

// Cancel preserves pending work while stopping the active ordinary Agent turn.
func (flow *sessionConversation) Cancel(requestContext context.Context, call rpc.Request[CancelRequest]) (rpc.Outcome[AcceptedValue], error) {
	subject, refused := flow.access.attachedAgent(requestContext, call.Payload.SessionID)
	if refused != nil {
		return rpc.Fail[AcceptedValue](*refused), nil
	}
	if err := subject.Cancel(requestContext, agent.UserCancel{}, agent.CancelOptions{
		KeepInbox: true,
	}); err != nil {
		return rpc.Outcome[AcceptedValue]{}, err
	}
	return rpc.OK(AcceptedValue{
		Accepted: true,
	}), nil
}

func canonicalClientTimeZone(input string) (string, error) {
	if input == "UTC" {
		return input, nil
	}
	if input == "" || input != filepath.Clean(input) || !ianaTimeZonePattern.MatchString(input) {
		return "", errors.New("invalid IANA time zone")
	}
	location, err := time.LoadLocation(input)
	if err != nil {
		return "", err
	}
	return location.String(), nil
}

func encodePromptSource(rpcID connection.RPCID, canonicalZone string) (json.RawMessage, error) {
	fields := struct {
		Kind           string           `json:"kind"`
		RPCID          connection.RPCID `json:"rpcId"`
		ClientTimeZone string           `json:"clientTimeZone,omitempty"`
	}{
		Kind:           "user",
		RPCID:          rpcID,
		ClientTimeZone: canonicalZone,
	}
	return json.Marshal(fields)
}

func locatePending(pending agent.Pending, identifier agentmessage.MessageID) (agentmessage.UserMessage, agent.InboxTarget, bool) {
	for _, candidate := range pending.NextTurn {
		if candidate.StableID() == identifier {
			return candidate, agent.NextTurn, true
		}
	}
	for _, candidate := range pending.NextStep {
		if candidate.StableID() == identifier {
			return candidate, agent.NextStep, true
		}
	}
	return agentmessage.UserMessage{}, "", false
}

func replaceMessageContent(messageValue agentmessage.UserMessage, content []json.RawMessage) (agentmessage.UserMessage, error) {
	origin, err := json.Marshal(messageValue.SourceValue())
	if err != nil {
		return agentmessage.UserMessage{}, err
	}
	if content == nil {
		content = []json.RawMessage{}
	}
	wireValue := struct {
		ID      agentmessage.MessageID   `json:"id"`
		Role    agentmessage.MessageRole `json:"role"`
		Content []json.RawMessage        `json:"content"`
		Source  json.RawMessage          `json:"source"`
	}{
		ID:      messageValue.StableID(),
		Role:    agentmessage.RoleUser,
		Content: content,
		Source:  origin,
	}
	encoded, err := json.Marshal(wireValue)
	if err != nil {
		return agentmessage.UserMessage{}, err
	}
	return agentmessage.DecodeUserMessage(encoded)
}

func queueItemNotFoundError(identifier sessionframes.MessageID) connection.RPCError {
	return rpc.NewRPCError(
		connection.ErrorQueueItemNotFound, "queued item is no longer pending",
		struct {
			ItemID sessionframes.MessageID `json:"itemId"`
		}{
			ItemID: identifier,
		},
	)
}

func queueFailure(identifier sessionframes.MessageID, failure error) (rpc.Outcome[AcceptedValue], error) {
	if errors.Is(failure, agent.ErrInputMissing) {
		return rpc.Fail[AcceptedValue](queueItemNotFoundError(identifier)), nil
	}
	if errors.Is(failure, agent.ErrSteerUnavailable) {
		return rpc.Fail[AcceptedValue](rpc.NewRPCError(connection.ErrorSteerUnavailable,
			"current turn no longer accepts steering", struct {
				ItemID sessionframes.MessageID `json:"itemId"`
			}{
				ItemID: identifier,
			},
		)), nil
	}
	return rpc.Outcome[AcceptedValue]{}, failure
}
