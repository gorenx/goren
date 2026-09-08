package apiactor

import (
	"context"
	"errors"

	"github.com/gorenx/goren/llm"
	llmactor "github.com/gorenx/goren/llm/actor"
	"github.com/gorenx/goren/session"
)

// Configuration reads the settings retained by LLM for this Agent identity.
func (source *Directory) Configuration(ctx context.Context, id session.SessionID) (llm.CallConfig, error) {
	payload, err := source.system.AskGrain(ctx, source.registry, llmactor.ReadConfiguration{
		AgentID: string(id),
	}, source.timeout)
	if err != nil {
		return llm.CallConfig{}, err
	}
	result, ok := payload.(llmactor.ConfigurationResult)
	if !ok {
		return llm.CallConfig{}, errors.New("apiproxy: invalid LLM configuration reply")
	}
	return result.Config, result.Err
}

// Configure replaces retained LLM settings without contacting an AgentLoop.
func (source *Directory) Configure(ctx context.Context, id session.SessionID, proposed llm.CallConfig) error {
	payload, err := source.system.AskGrain(ctx, source.registry, llmactor.SetConfiguration{
		AgentID: string(id),
		Config:  llm.CloneCallConfig(proposed),
	}, source.timeout)
	if err != nil {
		return err
	}
	result, ok := payload.(llmactor.ConfigurationResult)
	if !ok {
		return errors.New("apiproxy: invalid LLM configuration reply")
	}
	return result.Err
}
