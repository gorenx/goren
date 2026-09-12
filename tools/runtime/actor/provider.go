package runtimeactor

import (
	"errors"

	"github.com/gorenx/goren/userquestions"
	goakt "github.com/tochemey/goakt/v4/actor"
)

const providerID = "goren-tool-runtime-provider"

// Provider supplies process capabilities to Tool Runtimes. Composition installs
// dependencies before admitting Agents; Runtimes own all acquired MCP resources.
type Provider struct {
	questions userquestions.UserQuestions
}

// Install supplies the shared interaction capability once during startup.
func (source *Provider) Install(questions userquestions.UserQuestions) error {
	if questions == nil || source.questions != nil {
		return errors.New("tools: Runtime interaction capability is required and installed once")
	}
	source.questions = questions
	return nil
}

// ID identifies these dependencies in the GoAkt extension collection.
func (*Provider) ID() string { return providerID }

// Install provides Runtime dependencies during composition, before Agent admission.
func Install(system goakt.ActorSystem, questions userquestions.UserQuestions) error {
	if system == nil {
		return errors.New("tools: Runtime ActorSystem is required")
	}
	source, ok := system.Extension(providerID).(*Provider)
	if !ok {
		return errors.New("tools: Runtime Provider is not installed")
	}
	return source.Install(questions)
}
