package llm

import (
	"errors"
	"fmt"
)

func describeRoutes(providers []string, backend Adapter) ([]Binding, error) {
	unique := make(map[string]struct{}, len(providers))
	candidates := make([]Binding, 0, len(providers))
	for _, providerRoute := range providers {
		if providerRoute == "" {
			return nil, MustLlmError("adapter provider names must be non-empty", "INVALID_ADAPTER")
		}
		if _, exists := unique[providerRoute]; exists {
			return nil, MustLlmError(fmt.Sprintf("an adapter for provider %q is already registered", providerRoute), "DUPLICATE_ADAPTER")
		}
		unique[providerRoute] = struct{}{}
		metadata := ProviderInfo{
			ID:   providerRoute,
			Name: providerRoute,
		}
		if describer, ok := backend.(ProviderDescriber); ok {
			var err error
			metadata, err = describer.DescribeProvider(providerRoute)
			if err != nil {
				return nil, err
			}
		}
		if metadata.ID != providerRoute || metadata.Name == "" {
			return nil, MustLlmError(
				fmt.Sprintf("adapter metadata for provider %q must preserve its id and have a non-empty name", providerRoute),
				"INVALID_ADAPTER",
			)
		}
		policy, err := ResolveRetryPolicy(nil, fmt.Sprintf("llm: provider %q retryPolicy", providerRoute))
		if err != nil {
			return nil, err
		}
		if policySource, ok := backend.(RetryPolicyProvider); ok {
			candidatePolicy, policyErr := policySource.ProviderRetryPolicy(providerRoute)
			if policyErr != nil {
				return nil, policyErr
			}
			if candidatePolicy != nil {
				policy = candidatePolicy.CloneRetryPolicy()
			}
		}
		if err := validateResolvedPolicy(policy); err != nil {
			return nil, MustLlmError(fmt.Sprintf("invalid retry policy for provider %q: %v", providerRoute, err), "INVALID_ADAPTER")
		}
		candidates = append(candidates, Binding{
			backend:  backend,
			metadata: metadata,
			policy:   policy,
		})
	}
	return candidates, nil
}

func validateResolvedPolicy(policy RetryPolicy) error {
	if policy == nil {
		return errors.New("policy is nil")
	}
	backoff := policy.RetryBackoff()
	if !finitePositive(backoff.InitialDelayMS) || !finitePositive(backoff.MaxDelayMS) ||
		backoff.InitialDelayMS > backoff.MaxDelayMS || backoff.MaxDelayMS > MaxTimerDelayMS ||
		backoff.JitterRatio < 0 || backoff.JitterRatio > 1 {
		return errors.New("backoff is invalid")
	}
	switch typedPolicy := policy.(type) {
	case NormalRetryPolicy:
		if typedPolicy.MaxRetries < 0 || typedPolicy.MaxRetries > maxJavaScriptSafeInteger {
			return errors.New("maxRetries is invalid")
		}
		return validateRetryableCodes(typedPolicy.RetryableCodes, "retryPolicy")
	case AlwaysRetryPolicy:
		return nil
	default:
		return errors.New("policy implementation is unsupported")
	}
}
