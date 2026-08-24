// Package basic provides the source-aligned model-backed Compaction Provider.
package basic

import (
	"fmt"
	"math"
	"strings"
)

const (
	defaultThresholdRatio    = 0.8
	defaultRetainRatio       = 0.16
	defaultMaxTokens         = 8192
	defaultCompactionRetries = 1
	defaultOverflowRetries   = 1
)

// PolicyConfig contains fields shared by defaults and exact route overrides.
type PolicyConfig struct {
	ThresholdRatio        *float64 `json:"thresholdRatio,omitempty"`
	RetainRatio           *float64 `json:"retainRatio,omitempty"`
	RetainTokens          *int64   `json:"retainTokens,omitempty"`
	SummarizationProvider *string  `json:"summarizationProvider,omitempty"`
	SummarizationModel    *string  `json:"summarizationModel,omitempty"`
	MaxTokens             *int     `json:"maxTokens,omitempty"`
	CompactionRetries     *int     `json:"compactionRetries,omitempty"`
	MaxOverflowRetries    *int     `json:"maxOverflowRetries,omitempty"`
}

// ModelPolicyConfig overrides policy for one exact provider/model route.
type ModelPolicyConfig struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	PolicyConfig
}

// Config is the owner-defined Basic Provider configuration.
type Config struct {
	PolicyConfig
	ModelPolicies []ModelPolicyConfig `json:"modelPolicies,omitempty"`
	Auto          *bool               `json:"auto,omitempty"`
}

// Retention preserves exactly one ratio or absolute token form.
type Retention struct {
	Ratio  *float64
	Tokens *int64
}

// ResolvedConfig is a validated detached construction snapshot.
type ResolvedConfig struct {
	ThresholdRatio        float64
	Retention             Retention
	SummarizationProvider string
	SummarizationModel    string
	MaxTokens             int
	CompactionRetries     int
	MaxOverflowRetries    int
	ModelPolicies         []ModelPolicyConfig
	Auto                  bool
}

// RouteTarget is the exact durable provider/model route used for policy
// matching and pressure resolution.
type RouteTarget struct {
	Provider string
	Model    string
}

// ResolvedTargetPolicy is one exact-route override merged over service
// defaults before model-capacity scaling.
type ResolvedTargetPolicy struct {
	Target                RouteTarget
	ThresholdRatio        float64
	Retention             Retention
	SummarizationProvider string
	SummarizationModel    string
	MaxTokens             int
	CompactionRetries     int
	MaxOverflowRetries    int
}

// ResolvedCompactSpec contains concrete pressure and retained-tail budgets for
// one model capacity.
type ResolvedCompactSpec struct {
	Target                RouteTarget
	ContextWindow         int
	ThresholdRatio        float64
	ThresholdTokens       int64
	RetainTokens          int64
	SummarizationProvider string
	SummarizationModel    string
	MaxTokens             int
	CompactionRetries     int
	MaxOverflowRetries    int
}

// TargetPressureConfigError identifies one route whose model-capacity policy
// cannot produce valid concrete budgets.
type TargetPressureConfigError struct {
	TargetKey string
	Message   string
}

func (problem *TargetPressureConfigError) Error() string {
	if problem == nil {
		return "compaction-basic: invalid target pressure configuration"
	}
	return problem.Message
}

// policyCatalog is the immutable policy snapshot shared by the Provider's
// Runtime automation and Compaction implementation. It owns route resolution;
// neither collaborator reads configuration through the other.
type policyCatalog struct {
	settings ResolvedConfig
}

func newPolicyCatalog(settings ResolvedConfig) *policyCatalog {
	return &policyCatalog{
		settings: cloneResolvedConfig(settings),
	}
}

func (catalog *policyCatalog) automaticEnabled() bool {
	return catalog.settings.Auto
}

func (catalog *policyCatalog) resolve(selectedTarget RouteTarget) ResolvedTargetPolicy {
	return ResolveTargetPolicy(catalog.settings, selectedTarget)
}

func (catalog *policyCatalog) defaults() ResolvedTargetPolicy {
	return ResolvedTargetPolicy{
		ThresholdRatio:        catalog.settings.ThresholdRatio,
		Retention:             cloneRetention(catalog.settings.Retention),
		SummarizationProvider: catalog.settings.SummarizationProvider,
		SummarizationModel:    catalog.settings.SummarizationModel,
		MaxTokens:             catalog.settings.MaxTokens,
		CompactionRetries:     catalog.settings.CompactionRetries,
		MaxOverflowRetries:    catalog.settings.MaxOverflowRetries,
	}
}

// ResolveConfig validates construction fields and applies source defaults.
func ResolveConfig(settings Config) (ResolvedConfig, error) {
	threshold := defaultThresholdRatio
	if settings.ThresholdRatio != nil {
		threshold = *settings.ThresholdRatio
	}
	selectedRetention := Retention{
		Ratio: floatPointer(defaultRetainRatio),
	}
	if settings.RetainRatio != nil {
		selectedRetention = Retention{
			Ratio: floatPointer(*settings.RetainRatio),
		}
	}
	if settings.RetainTokens != nil {
		selectedRetention = Retention{
			Tokens: int64Pointer(*settings.RetainTokens),
		}
	}
	if err := validatePolicy(settings.PolicyConfig, threshold, selectedRetention, "BasicCompactionConfig"); err != nil {
		return ResolvedConfig{}, err
	}
	provider, model, err := summarizationPair(settings.PolicyConfig, "BasicCompactionConfig")
	if err != nil {
		return ResolvedConfig{}, err
	}
	maxTokens := defaultMaxTokens
	if settings.MaxTokens != nil {
		maxTokens = *settings.MaxTokens
	}
	compactionRetries := defaultCompactionRetries
	if settings.CompactionRetries != nil {
		compactionRetries = *settings.CompactionRetries
	}
	overflowRetries := defaultOverflowRetries
	if settings.MaxOverflowRetries != nil {
		overflowRetries = *settings.MaxOverflowRetries
	}
	auto := true
	if settings.Auto != nil {
		auto = *settings.Auto
	}
	policies, err := validateModelPolicies(settings.ModelPolicies, threshold, selectedRetention)
	if err != nil {
		return ResolvedConfig{}, err
	}
	return ResolvedConfig{
		ThresholdRatio:        threshold,
		Retention:             selectedRetention,
		SummarizationProvider: provider,
		SummarizationModel:    model,
		MaxTokens:             maxTokens,
		CompactionRetries:     compactionRetries,
		MaxOverflowRetries:    overflowRetries,
		ModelPolicies:         policies,
		Auto:                  auto,
	}, nil
}

// ResolveTargetPolicy merges the exact provider/model override over validated
// service defaults.
func ResolveTargetPolicy(
	settings ResolvedConfig,
	selectedTarget RouteTarget,
) ResolvedTargetPolicy {
	resolved := ResolvedTargetPolicy{
		Target: RouteTarget{
			Provider: selectedTarget.Provider,
			Model:    selectedTarget.Model,
		},
		ThresholdRatio:        settings.ThresholdRatio,
		Retention:             cloneRetention(settings.Retention),
		SummarizationProvider: settings.SummarizationProvider,
		SummarizationModel:    settings.SummarizationModel,
		MaxTokens:             settings.MaxTokens,
		CompactionRetries:     settings.CompactionRetries,
		MaxOverflowRetries:    settings.MaxOverflowRetries,
	}
	for _, override := range settings.ModelPolicies {
		if override.Provider != selectedTarget.Provider ||
			override.Model != selectedTarget.Model {
			continue
		}
		if override.ThresholdRatio != nil {
			resolved.ThresholdRatio = *override.ThresholdRatio
		}
		if override.RetainTokens != nil {
			resolved.Retention = Retention{
				Tokens: int64Pointer(*override.RetainTokens),
			}
		} else if override.RetainRatio != nil {
			resolved.Retention = Retention{
				Ratio: floatPointer(*override.RetainRatio),
			}
		}
		if override.SummarizationProvider != nil {
			resolved.SummarizationProvider = *override.SummarizationProvider
			resolved.SummarizationModel = *override.SummarizationModel
		}
		if override.MaxTokens != nil {
			resolved.MaxTokens = *override.MaxTokens
		}
		if override.CompactionRetries != nil {
			resolved.CompactionRetries = *override.CompactionRetries
		}
		if override.MaxOverflowRetries != nil {
			resolved.MaxOverflowRetries = *override.MaxOverflowRetries
		}
		break
	}
	return resolved
}

// ResolveCompactSpec scales one exact-route policy into concrete token
// budgets for the adapter-owned context window.
func ResolveCompactSpec(
	resolved ResolvedTargetPolicy,
	contextWindow int,
) (ResolvedCompactSpec, error) {
	targetKey := resolved.Target.Provider + "/" + resolved.Target.Model
	if contextWindow <= 0 {
		return ResolvedCompactSpec{}, &TargetPressureConfigError{
			TargetKey: targetKey,
			Message: fmt.Sprintf(
				"BasicCompactionConfig: contextWindow (%d) must be a positive integer",
				contextWindow,
			),
		}
	}
	thresholdTokens := int64(math.Floor(
		float64(contextWindow) * resolved.ThresholdRatio,
	))
	retainTokens := int64(0)
	if resolved.Retention.Tokens != nil {
		retainTokens = *resolved.Retention.Tokens
	} else if resolved.Retention.Ratio != nil {
		retainTokens = int64(math.Floor(
			float64(contextWindow) * *resolved.Retention.Ratio,
		))
	}
	if retainTokens >= thresholdTokens {
		return ResolvedCompactSpec{}, &TargetPressureConfigError{
			TargetKey: targetKey,
			Message: fmt.Sprintf(
				"BasicCompactionConfig: %s retainTokens (%d) must be less than threshold tokens %d",
				targetKey,
				retainTokens,
				thresholdTokens,
			),
		}
	}
	return ResolvedCompactSpec{
		Target: RouteTarget{
			Provider: resolved.Target.Provider,
			Model:    resolved.Target.Model,
		},
		ContextWindow:         contextWindow,
		ThresholdRatio:        resolved.ThresholdRatio,
		ThresholdTokens:       thresholdTokens,
		RetainTokens:          retainTokens,
		SummarizationProvider: resolved.SummarizationProvider,
		SummarizationModel:    resolved.SummarizationModel,
		MaxTokens:             resolved.MaxTokens,
		CompactionRetries:     resolved.CompactionRetries,
		MaxOverflowRetries:    resolved.MaxOverflowRetries,
	}, nil
}

func validatePolicy(
	policy PolicyConfig,
	resolvedThreshold float64,
	resolvedRetention Retention,
	name string,
) error {
	if !validRatio(resolvedThreshold) {
		return fmt.Errorf("%s: thresholdRatio must be in (0, 1]", name)
	}
	if policy.RetainRatio != nil && policy.RetainTokens != nil {
		return fmt.Errorf("%s: retainRatio and retainTokens are mutually exclusive", name)
	}
	if resolvedRetention.Ratio != nil {
		if !validRatio(*resolvedRetention.Ratio) || *resolvedRetention.Ratio >= resolvedThreshold {
			return fmt.Errorf("%s: retainRatio must be positive and less than thresholdRatio", name)
		}
	}
	if resolvedRetention.Tokens != nil && *resolvedRetention.Tokens < 0 {
		return fmt.Errorf("%s: retainTokens must be non-negative", name)
	}
	if policy.MaxTokens != nil && *policy.MaxTokens <= 0 {
		return fmt.Errorf("%s: maxTokens must be positive", name)
	}
	if policy.CompactionRetries != nil && *policy.CompactionRetries < 0 {
		return fmt.Errorf("%s: compactionRetries must be non-negative", name)
	}
	if policy.MaxOverflowRetries != nil && *policy.MaxOverflowRetries < 0 {
		return fmt.Errorf("%s: maxOverflowRetries must be non-negative", name)
	}
	_, _, err := summarizationPair(policy, name)
	return err
}

func validateModelPolicies(
	configured []ModelPolicyConfig,
	defaultThreshold float64,
	defaultRetention Retention,
) ([]ModelPolicyConfig, error) {
	seen := make(map[string]struct{}, len(configured))
	validated := make([]ModelPolicyConfig, len(configured))
	for index, policy := range configured {
		name := fmt.Sprintf("BasicCompactionConfig: modelPolicies[%d]", index)
		if strings.TrimSpace(policy.Provider) == "" || strings.TrimSpace(policy.Model) == "" {
			return nil, fmt.Errorf("%s: provider and model must be non-empty", name)
		}
		key := policy.Provider + "\x00" + policy.Model
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("BasicCompactionConfig: duplicate model policy for %s/%s", policy.Provider, policy.Model)
		}
		seen[key] = struct{}{}
		threshold := defaultThreshold
		if policy.ThresholdRatio != nil {
			threshold = *policy.ThresholdRatio
		}
		selectedRetention := defaultRetention
		if policy.RetainRatio != nil {
			selectedRetention = Retention{
				Ratio: floatPointer(*policy.RetainRatio),
			}
		}
		if policy.RetainTokens != nil {
			selectedRetention = Retention{
				Tokens: int64Pointer(*policy.RetainTokens),
			}
		}
		if err := validatePolicy(policy.PolicyConfig, threshold, selectedRetention, name); err != nil {
			return nil, err
		}
		validated[index] = cloneModelPolicy(policy)
	}
	return validated, nil
}

func summarizationPair(policy PolicyConfig, name string) (string, string, error) {
	if policy.SummarizationProvider == nil && policy.SummarizationModel == nil {
		return "", "", nil
	}
	if policy.SummarizationProvider == nil || policy.SummarizationModel == nil {
		return "", "", fmt.Errorf("%s: summarizationProvider and summarizationModel must be set together", name)
	}
	provider := *policy.SummarizationProvider
	model := *policy.SummarizationModel
	if (provider == "") != (model == "") {
		return "", "", fmt.Errorf("%s: summarizationProvider and summarizationModel must be an empty or non-empty pair", name)
	}
	return provider, model, nil
}

func validRatio(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value > 0 && value <= 1
}

func cloneModelPolicy(source ModelPolicyConfig) ModelPolicyConfig {
	detached := source
	detached.ThresholdRatio = cloneFloat(source.ThresholdRatio)
	detached.RetainRatio = cloneFloat(source.RetainRatio)
	detached.RetainTokens = cloneInt64(source.RetainTokens)
	detached.SummarizationProvider = cloneString(source.SummarizationProvider)
	detached.SummarizationModel = cloneString(source.SummarizationModel)
	detached.MaxTokens = cloneInt(source.MaxTokens)
	detached.CompactionRetries = cloneInt(source.CompactionRetries)
	detached.MaxOverflowRetries = cloneInt(source.MaxOverflowRetries)
	return detached
}

func cloneResolvedConfig(source ResolvedConfig) ResolvedConfig {
	detached := source
	detached.Retention = cloneRetention(source.Retention)
	if source.ModelPolicies != nil {
		detached.ModelPolicies = make([]ModelPolicyConfig, len(source.ModelPolicies))
		for index, entry := range source.ModelPolicies {
			detached.ModelPolicies[index] = cloneModelPolicy(entry)
		}
	}
	return detached
}

func cloneRetention(source Retention) Retention {
	return Retention{
		Ratio:  cloneFloat(source.Ratio),
		Tokens: cloneInt64(source.Tokens),
	}
}

func cloneFloat(source *float64) *float64 {
	if source == nil {
		return nil
	}
	return floatPointer(*source)
}

func cloneInt(source *int) *int {
	if source == nil {
		return nil
	}
	detached := *source
	return &detached
}

func cloneInt64(source *int64) *int64 {
	if source == nil {
		return nil
	}
	return int64Pointer(*source)
}

func cloneString(source *string) *string {
	if source == nil {
		return nil
	}
	detached := *source
	return &detached
}

func floatPointer(value float64) *float64 {
	return &value
}

func int64Pointer(value int64) *int64 {
	return &value
}
