// Package userquestions owns structured human questions, answer validation
// prerequisites, and the single active answer-provider capability.
package userquestions

import (
	"context"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/plugin"
)

const ServiceName = "userQuestions"

// PluginName is the canonical Harness Plugin name.
const PluginName = "@deepseek-ai/dsh-user-questions"

// Option is one selectable user-facing answer.
type Option struct {
	Label       string  `json:"label"`
	Description *string `json:"description,omitempty"`
}

// IntentKind is the closed presentation-intent vocabulary.
type IntentKind string

const IntentPlanReview IntentKind = "plan-review"

// Intent changes presentation only; answer encoding remains unchanged.
type Intent struct {
	Kind    IntentKind `json:"kind"`
	Approve string     `json:"approve"`
}

// Question is one member of a human-answer request batch.
type Question struct {
	ID          string    `json:"id"`
	Question    string    `json:"question"`
	Detail      *string   `json:"detail,omitempty"`
	Header      *string   `json:"header,omitempty"`
	Options     *[]Option `json:"options,omitempty"`
	MultiSelect *bool     `json:"multiSelect,omitempty"`
	Intent      *Intent   `json:"intent,omitempty"`
}

// AnswerItem is the answer to one question.
type AnswerItem struct {
	ID       string   `json:"id"`
	Selected []string `json:"selected"`
	Custom   *string  `json:"custom,omitempty"`
}

// Answer resolves one question batch atomically.
type Answer struct {
	Answers []AnswerItem `json:"answers"`
}

// Request borrows questions and the exact live calling Agent for one ask.
type Request struct {
	Questions []Question
	Subject   agent.Agent
}

// Provider collects one human answer batch.
type Provider interface {
	Ask(context.Context, Request) (Answer, error)
}

// UserQuestions is the provider-owned question capability.
type UserQuestions interface {
	plugin.Service
	RegisterProvider(Provider) (*ProviderHandle, error)
	Ask(context.Context, Request) (Answer, error)
}

func cloneQuestions(entries []Question) []Question {
	if entries == nil {
		return nil
	}
	detached := make([]Question, len(entries))
	for index, entry := range entries {
		detached[index] = entry
		detached[index].Detail = cloneString(entry.Detail)
		detached[index].Header = cloneString(entry.Header)
		detached[index].MultiSelect = cloneBool(entry.MultiSelect)
		if entry.Options != nil {
			options := make([]Option, len(*entry.Options))
			for optionIndex, offeredChoice := range *entry.Options {
				options[optionIndex] = offeredChoice
				options[optionIndex].Description = cloneString(offeredChoice.Description)
			}
			detached[index].Options = &options
		}
		if entry.Intent != nil {
			intentSnapshot := *entry.Intent
			detached[index].Intent = &intentSnapshot
		}
	}
	return detached
}

func cloneAnswer(source Answer) Answer {
	detached := Answer{
		Answers: make([]AnswerItem, len(source.Answers)),
	}
	if source.Answers == nil {
		detached.Answers = nil
	}
	for index, entry := range source.Answers {
		detached.Answers[index] = entry
		detached.Answers[index].Selected = append([]string(nil), entry.Selected...)
		if entry.Selected != nil && detached.Answers[index].Selected == nil {
			detached.Answers[index].Selected = []string{}
		}
		detached.Answers[index].Custom = cloneString(entry.Custom)
	}
	return detached
}

func cloneString(source *string) *string {
	if source == nil {
		return nil
	}
	snapshot := *source
	return &snapshot
}

func cloneBool(source *bool) *bool {
	if source == nil {
		return nil
	}
	snapshot := *source
	return &snapshot
}
