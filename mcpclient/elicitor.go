package mcpclient

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"

	"github.com/gorenx/goren/userquestions"
	"github.com/mark3labs/mcp-go/mcp"
)

type elicitor struct {
	serverName string
	questions  userquestions.UserQuestions
}

func (bridge *elicitor) Elicit(ctx context.Context, request mcp.ElicitationRequest) (*mcp.ElicitationResult, error) {
	origin, valid := ctx.Value(callerKey{}).(caller)
	if !valid ||
		origin.sessionID == "" ||
		origin.callID == "" ||
		bridge.questions == nil {
		return elicitationResult("decline", nil), nil
	}
	if ctx.Err() != nil {
		return elicitationResult("cancel", nil), nil
	}
	if request.Params.Validate() != nil {
		return elicitationResult("decline", nil), nil
	}
	if request.Params.Mode == mcp.ElicitationModeURL {
		address, err := url.Parse(request.Params.URL)
		if err != nil || !safeElicitationURL(address) {
			return elicitationResult("decline", nil), nil
		}
		detail := "Server: " + bridge.serverName + "\nHost: " + address.Host + "\nURL: " + request.Params.URL + "\nOpen this address yourself, then select Continue."
		action := bridge.consent(ctx, origin, request.Params.Message, detail)
		return elicitationResult(action, nil), nil
	}
	prepared, err := newForm(request.Params.RequestedSchema)
	if err != nil {
		return elicitationResult("decline", nil), nil
	}
	names := make([]string, len(prepared.fields))
	for index, field := range prepared.fields {
		names[index] = field.name
	}
	detail := "Server: " + bridge.serverName + "\nRequested fields: " + strings.Join(names, ", ")
	action := bridge.consent(ctx, origin, request.Params.Message, detail)
	if action != "accept" {
		return elicitationResult(action, nil), nil
	}
	invalidAnswer := false
	for {
		if ctx.Err() != nil {
			return elicitationResult("cancel", nil), nil
		}
		if len(prepared.fields) == 0 {
			return elicitationResult("accept", map[string]any{}), nil
		}
		batch := prepared.questionBatch()
		if invalidAnswer {
			for index := range batch {
				detail := "The previous answer did not satisfy the requested fields. Please correct it.\n" + *batch[index].Detail
				batch[index].Detail = &detail
			}
		}
		answerValue, err := bridge.questions.Ask(ctx, userquestions.Request{
			SessionID: origin.sessionID,
			Questions: batch,
		})
		if err != nil {
			return elicitationResult(questionAction(err), nil), nil
		}
		content, err := prepared.answer(answerValue)
		if err == nil {
			return elicitationResult("accept", content), nil
		}
		invalidAnswer = true
		// Invalid answers never leave Goren. The same fields are asked again;
		// their schema constraints remain visible and the overall call is bounded.
	}
}

func (bridge *elicitor) consent(ctx context.Context, origin caller, message string, detail string) mcp.ElicitationResponseAction {
	choices := []userquestions.Option{
		{
			Label: "Continue",
		},
		{
			Label: "Decline",
		},
		{
			Label: "Cancel",
		},
	}
	answerValue, err := bridge.questions.Ask(ctx, userquestions.Request{
		SessionID: origin.sessionID,
		Questions: []userquestions.Question{
			{
				ID:       "mcp-consent",
				Question: message,
				Detail:   &detail,
				Options:  &choices,
			},
		},
	})
	if ctx.Err() != nil {
		return "cancel"
	}
	if err != nil {
		return questionAction(err)
	}
	if len(answerValue.Answers) != 1 {
		return "cancel"
	}
	item := answerValue.Answers[0]
	if item.ID != "mcp-consent" ||
		len(item.Selected) != 1 ||
		item.Custom != nil {
		return "cancel"
	}
	switch item.Selected[0] {
	case "Continue":
		return "accept"
	case "Decline":
		return "decline"
	default:
		return "cancel"
	}
}

func elicitationResult(action mcp.ElicitationResponseAction, content map[string]any) *mcp.ElicitationResult {
	return &mcp.ElicitationResult{
		ElicitationResponse: mcp.ElicitationResponse{
			Action:  action,
			Content: content,
		},
	}
}

func questionAction(err error) mcp.ElicitationResponseAction {
	var problem *userquestions.Error
	if errors.As(err, &problem) {
		switch problem.Code {
		case userquestions.CodeNoProvider, userquestions.CodeDelegatedCaller, userquestions.CodeCallerNotLive, userquestions.CodeMissingAgent:
			return "decline"
		}
	}
	return "cancel"
}

func safeElicitationURL(address *url.URL) bool {
	if address.User != nil ||
		address.Hostname() == "" ||
		address.Opaque != "" {
		return false
	}
	if address.Scheme == "https" {
		return true
	}
	if address.Scheme != "http" {
		return false
	}
	ip := net.ParseIP(address.Hostname())
	return address.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
}
