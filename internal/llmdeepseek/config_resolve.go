package llmdeepseek

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/gorenx/goren/llm"
)

// ResolveOptions validates one whole connection generation atomically.
func ResolveOptions(settings Config, launchEnvironment Environment) (ConnectionOptions, error) {
	if settings.Thinking != nil {
		if *settings.Thinking != ThinkingEnabled && *settings.Thinking != ThinkingDisabled {
			return ConnectionOptions{}, errors.New("llm-deepseek: thinking must be enabled or disabled")
		}
	}
	if settings.ReasoningEffort != nil {
		switch *settings.ReasoningEffort {
		case ReasoningOff, ReasoningHigh, ReasoningMax:
		default:
			return ConnectionOptions{}, errors.New("llm-deepseek: reasoningEffort must be off, high, or max")
		}
	}
	if settings.Thinking != nil && *settings.Thinking == ThinkingDisabled &&
		settings.ReasoningEffort != nil && *settings.ReasoningEffort != ReasoningOff {
		return ConnectionOptions{}, errors.New("llm-deepseek: only reasoningEffort off can be configured when thinking is disabled")
	}
	credentialRef := DefaultAPIKeyEnv
	if settings.APIKeyEnv != nil {
		credentialRef = *settings.APIKeyEnv
	}
	if strings.TrimSpace(credentialRef) == "" || credentialRef != strings.TrimSpace(credentialRef) {
		return ConnectionOptions{}, errors.New("llm-deepseek: apiKeyEnv must be non-empty and trimmed")
	}
	baseURL := ""
	if settings.BaseURL != nil {
		baseURL = *settings.BaseURL
	} else if launchEnvironment.LookupEnv != nil {
		baseURL, _ = launchEnvironment.LookupEnv(BaseURLEnv)
	}
	if baseURL == "" {
		baseURL = PublicBaseURL
	}
	maximumTokens := DefaultMaxTokens
	if settings.MaxTokens != nil {
		maximumTokens = *settings.MaxTokens
	}
	if maximumTokens <= 0 || int64(maximumTokens) > maxJavaScriptSafeInteger {
		return ConnectionOptions{}, errors.New("llm-deepseek: maxTokens must be a positive safe integer")
	}
	contextWindow := DefaultContextWindow
	if settings.DefaultContextWindow != nil {
		contextWindow = *settings.DefaultContextWindow
	}
	if contextWindow <= 0 {
		return ConnectionOptions{}, errors.New("llm-deepseek: defaultContextWindow must be a positive integer")
	}
	modelCatalog, err := resolveModels(settings.Models)
	if err != nil {
		return ConnectionOptions{}, err
	}
	idleMilliseconds := float64(DefaultStreamIdleTimeoutMS)
	if settings.StreamIdleTimeoutMS != nil {
		idleMilliseconds = *settings.StreamIdleTimeoutMS
	}
	if math.IsNaN(idleMilliseconds) || math.IsInf(idleMilliseconds, 0) ||
		idleMilliseconds <= 0 || idleMilliseconds > llm.MaxTimerDelayMS {
		return ConnectionOptions{}, fmt.Errorf(
			"llm-deepseek: streamIdleTimeoutMs must be positive and no greater than %.0f", llm.MaxTimerDelayMS,
		)
	}
	policy, err := llm.ResolveRetryPolicy(settings.RetryPolicy, "llm-deepseek: retryPolicy")
	if err != nil {
		return ConnectionOptions{}, err
	}
	return ConnectionOptions{
		APIKeyEnv: credentialRef, BaseURL: baseURL,
		Defaults:  RequestDefaults{Thinking: cloneThinking(settings.Thinking), ReasoningEffort: cloneReasoningEffort(settings.ReasoningEffort)},
		MaxTokens: maximumTokens, DefaultContextWindow: contextWindow, Models: modelCatalog,
		StreamIdleTimeout: time.Duration(idleMilliseconds * float64(time.Millisecond)), RetryPolicy: policy,
	}, nil
}

func resolveModels(configured *[]CatalogModel) ([]CatalogModel, error) {
	models := []CatalogModel{
		{ID: DefaultModelID, Name: stringPointer("DeepSeek-V4-Flash"), ContextWindow: intPointer(DefaultContextWindow)},
		{ID: "deepseek-v4-pro", Name: stringPointer("DeepSeek-V4-Pro"), ContextWindow: intPointer(DefaultContextWindow)},
	}
	if configured != nil {
		models = slices.Clone(*configured)
	}
	seen := make(map[string]struct{}, len(models))
	for index := range models {
		candidate := models[index]
		if candidate.ID == "" {
			return nil, errors.New("llm-deepseek: catalog model ids must be non-empty")
		}
		if candidate.Name != nil && *candidate.Name == "" {
			return nil, fmt.Errorf("llm-deepseek: catalog model %q has an empty name", candidate.ID)
		}
		if candidate.ContextWindow != nil && *candidate.ContextWindow <= 0 {
			return nil, fmt.Errorf("llm-deepseek: catalog model %q contextWindow must be a positive integer", candidate.ID)
		}
		if candidate.MaxTokens != nil && *candidate.MaxTokens <= 0 {
			return nil, fmt.Errorf("llm-deepseek: catalog model %q maxTokens must be a positive integer", candidate.ID)
		}
		if _, exists := seen[candidate.ID]; exists {
			return nil, fmt.Errorf("llm-deepseek: duplicate catalog model %q", candidate.ID)
		}
		seen[candidate.ID] = struct{}{}
		candidate.ContextWindow = cloneInt(candidate.ContextWindow)
		candidate.MaxTokens = cloneInt(candidate.MaxTokens)
		candidate.Name = cloneString(candidate.Name)
		candidate.Description = cloneString(candidate.Description)
		models[index] = candidate
	}
	return models, nil
}
