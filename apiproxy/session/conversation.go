package sessionapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"time"

	"github.com/gorenx/goren/agent"
	api "github.com/gorenx/goren/apiproxy"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/llm"
)

var ianaTimeZonePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+.-]*(?:/[A-Za-z0-9_+.-]+)+$`)

type sessionConversation struct {
	access  *sessionAccess
	runtime llm.LlmRuntime
}

func (flow *sessionConversation) Prompt(
	requestContext context.Context,
	call api.Request[api.SessionPromptRequest],
) (api.Outcome[api.SessionPromptValue], error) {
	subject, refused := flow.access.ordinaryAgent(requestContext, call.Payload.SessionID)
	if refused != nil {
		return api.Fail[api.SessionPromptValue](*refused), nil
	}
	selectionRef, err := flow.access.runtimeSessions.Selection(requestContext, subject)
	if err != nil {
		return api.Outcome[api.SessionPromptValue]{}, err
	}
	current, _, err := selectionRef.Current()
	if err != nil {
		return api.Outcome[api.SessionPromptValue]{}, err
	}
	if !slices.ContainsFunc(
		flow.runtime.ListProviders(),
		func(provider llm.ProviderInfo) bool {
			return provider.ID == current.Provider
		}) {
		return api.Fail[api.SessionPromptValue](
			api.NewRPCError(
				connection.ErrorModelUnavailable,
				fmt.Sprintf("no adapter serves provider %q; select a model for this session", current.Provider),
				struct {
					Provider string `json:"provider"`
					Model    string `json:"model"`
				}{Provider: current.Provider, Model: current.Model},
			),
		), nil
	}
	canonicalZone := ""
	if call.Payload.ClientTimeZone != nil {
		canonicalZone, err = canonicalClientTimeZone(*call.Payload.ClientTimeZone)
		if err != nil {
			return api.Fail[api.SessionPromptValue](api.NewRPCError(
				connection.ErrorInvalidTimeZone,
				"clientTimeZone must be UTC or a valid IANA Area/Location name",
				struct {
					Value string `json:"value"`
				}{Value: *call.Payload.ClientTimeZone},
			)), nil
		}
	}
	content := make([]llm.ContentBlock, 0, len(call.Payload.Content))
	for _, part := range call.Payload.Content {
		switch typedPart := part.(type) {
		case api.PromptTextPart:
			content = append(content, llm.NewTextBlock(typedPart.Text))
		case api.PromptImagePart:
			return api.Fail[api.SessionPromptValue](api.NewRPCError(
				connection.ErrorAttachment,
				"Image input is unavailable: this deployment mounts no attachment service.",
				struct {
					Reason string `json:"reason"`
				}{Reason: "ATTACHMENT_UNAVAILABLE"},
			)), nil
		}
	}
	originJSON, err := encodePromptSource(call.RPCID, canonicalZone)
	if err != nil {
		return api.Outcome[api.SessionPromptValue]{}, err
	}
	origin, err := llm.NewOpaqueMessageSource("user", originJSON)
	if err != nil {
		return api.Outcome[api.SessionPromptValue]{}, err
	}
	messageValue, err := llm.NewUserMessage(llm.UserMessageInput{Content: content, Source: origin})
	if err != nil {
		return api.Outcome[api.SessionPromptValue]{}, err
	}
	if call.Payload.Mode == "steer" {
		err = subject.Steer(messageValue)
	} else {
		err = subject.Followup(messageValue)
	}
	if err != nil {
		return api.Fail[api.SessionPromptValue](api.NewRPCError(
			connection.ErrorAgentBusy, "prompt rejected", struct {
				Reason string `json:"reason"`
			}{Reason: err.Error()},
		)), nil
	}
	return api.OK(api.SessionPromptValue{Accepted: true}), nil
}

// UpdateQueue edits, removes, or strictly steers one still-pending occurrence.
func (flow *sessionConversation) UpdateQueue(requestContext context.Context, call api.Request[api.SessionUpdateQueueRequest]) (api.Outcome[api.AcceptedValue], error) {
	subject, refused := flow.access.ordinaryAgent(requestContext, call.Payload.SessionID)
	if refused != nil {
		if refused.Code == connection.ErrorSessionNotFound {
			return api.Fail[api.AcceptedValue](queueItemNotFoundError(call.Payload.ItemID)), nil
		}
		return api.Fail[api.AcceptedValue](*refused), nil
	}
	pendingID := llm.MessageID(call.Payload.ItemID)
	pending := subject.InboxValue()
	messageValue, target, found := locatePending(pending, pendingID)
	if !found {
		return api.Fail[api.AcceptedValue](queueItemNotFoundError(call.Payload.ItemID)), nil
	}
	switch action := call.Payload.Action.(type) {
	case api.EditQueueAction:
		for _, rawBlock := range action.Content {
			var header struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(rawBlock, &header)
			if header.Type != "text" {
				return api.Fail[api.AcceptedValue](api.NewRPCError(
					connection.ErrorAttachment, "queue edits accept text content only", struct {
						Reason string `json:"reason"`
					}{Reason: "QUEUE_EDIT_NON_TEXT"},
				)), nil
			}
		}
		replacement, err := replaceMessageContent(messageValue, action.Content)
		if err != nil {
			return api.Outcome[api.AcceptedValue]{}, err
		}
		if _, err := pending.Replace(pendingID, replacement); err != nil {
			return api.Outcome[api.AcceptedValue]{}, err
		}
	case api.RemoveQueueAction:
		if _, err := pending.Remove(pendingID); err != nil {
			return api.Outcome[api.AcceptedValue]{}, err
		}
	case api.SteerQueueAction:
		if target != agent.NextTurn || subject.StatusValue() != agent.StatusRunning {
			return api.Fail[api.AcceptedValue](api.NewRPCError(
				connection.ErrorSteerUnavailable,
				"current turn no longer accepts steering",
				struct {
					ItemID api.MessageID `json:"itemId"`
				}{ItemID: call.Payload.ItemID},
			)), nil
		}
		if _, err := pending.Remove(pendingID); err != nil {
			return api.Outcome[api.AcceptedValue]{}, err
		}
		if err := subject.Steer(messageValue); err != nil {
			return api.Outcome[api.AcceptedValue]{}, err
		}
	}
	return api.OK(api.AcceptedValue{Accepted: true}), nil
}

// Cancel preserves pending work while stopping the active ordinary Agent turn.
func (flow *sessionConversation) Cancel(requestContext context.Context, call api.Request[api.SessionCancelRequest]) (api.Outcome[api.AcceptedValue], error) {
	subject, refused := flow.access.ordinaryAgent(requestContext, call.Payload.SessionID)
	if refused != nil {
		return api.Fail[api.AcceptedValue](*refused), nil
	}
	subject.Cancel(agent.UserCancel{}, agent.CancelOptions{KeepInbox: true})
	return api.OK(api.AcceptedValue{Accepted: true}), nil
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
	}{Kind: "user", RPCID: rpcID, ClientTimeZone: canonicalZone}
	return json.Marshal(fields)
}

func locatePending(pending *agent.Inbox, identifier llm.MessageID) (llm.UserMessage, agent.InboxTarget, bool) {
	for _, candidate := range pending.NextTurn() {
		if candidate.StableID() == identifier {
			return candidate, agent.NextTurn, true
		}
	}
	for _, candidate := range pending.NextStep() {
		if candidate.StableID() == identifier {
			return candidate, agent.NextStep, true
		}
	}
	return llm.UserMessage{}, "", false
}

func replaceMessageContent(messageValue llm.UserMessage, content []json.RawMessage) (llm.UserMessage, error) {
	origin, err := json.Marshal(messageValue.SourceValue())
	if err != nil {
		return llm.UserMessage{}, err
	}
	if content == nil {
		content = []json.RawMessage{}
	}
	wireValue := struct {
		ID      llm.MessageID     `json:"id"`
		Role    llm.MessageRole   `json:"role"`
		Content []json.RawMessage `json:"content"`
		Source  json.RawMessage   `json:"source"`
	}{
		ID: messageValue.StableID(), Role: llm.RoleUser,
		Content: content, Source: origin,
	}
	encoded, err := json.Marshal(wireValue)
	if err != nil {
		return llm.UserMessage{}, err
	}
	return llm.DecodeUserMessage(encoded)
}

func queueItemNotFoundError(identifier api.MessageID) connection.RPCError {
	return api.NewRPCError(
		connection.ErrorQueueItemNotFound, "queued item is no longer pending",
		struct {
			ItemID api.MessageID `json:"itemId"`
		}{ItemID: identifier},
	)
}
