package basic

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestResolveConfigDefaultsAndDetachesInput(t *testing.T) {
	t.Parallel()
	provider := "summary-provider"
	model := "summary-model"
	threshold := 0.6
	retained := int64(120)
	maximum := 4096
	retries := 2
	overflowRetries := 3
	autoSetting := false
	settings := Config{
		PolicyConfig: PolicyConfig{
			SummarizationProvider: &provider,
			SummarizationModel:    &model,
		},
		ModelPolicies: []ModelPolicyConfig{
			{
				Provider: "route",
				Model:    "large",
				PolicyConfig: PolicyConfig{
					ThresholdRatio:     &threshold,
					RetainTokens:       &retained,
					MaxTokens:          &maximum,
					CompactionRetries:  &retries,
					MaxOverflowRetries: &overflowRetries,
				},
			},
		},
		Auto: &autoSetting,
	}
	resolved, err := ResolveConfig(settings)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ThresholdRatio != defaultThresholdRatio ||
		resolved.Retention.Ratio == nil ||
		*resolved.Retention.Ratio != defaultRetainRatio ||
		resolved.MaxTokens != defaultMaxTokens ||
		resolved.CompactionRetries != defaultCompactionRetries ||
		resolved.MaxOverflowRetries != defaultOverflowRetries ||
		resolved.Auto {
		t.Fatalf("resolved defaults = %#v", resolved)
	}
	settings.ModelPolicies[0].Provider = "mutated"
	threshold = 0.2
	retained = 999
	provider = "mutated"
	if resolved.ModelPolicies[0].Provider != "route" ||
		*resolved.ModelPolicies[0].ThresholdRatio != 0.6 ||
		*resolved.ModelPolicies[0].RetainTokens != 120 ||
		resolved.SummarizationProvider != "summary-provider" {
		t.Fatalf("resolved aliases input = %#v", resolved)
	}
}

func TestResolveTargetPolicyMergesExactRoute(t *testing.T) {
	t.Parallel()
	threshold := 0.6
	retained := int64(400)
	provider := "summary"
	model := "summary-model"
	maximum := 2048
	retries := 4
	overflowRetries := 3
	resolved, err := ResolveConfig(Config{
		PolicyConfig: PolicyConfig{
			RetainTokens: int64Pointer(200),
		},
		ModelPolicies: []ModelPolicyConfig{
			{
				Provider: "deepseek",
				Model:    "chat",
				PolicyConfig: PolicyConfig{
					ThresholdRatio:        &threshold,
					RetainTokens:          &retained,
					SummarizationProvider: &provider,
					SummarizationModel:    &model,
					MaxTokens:             &maximum,
					CompactionRetries:     &retries,
					MaxOverflowRetries:    &overflowRetries,
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	target := RouteTarget{
		Provider: "deepseek",
		Model:    "chat",
	}
	policy := ResolveTargetPolicy(resolved, target)
	if policy.Target != target || policy.ThresholdRatio != 0.6 ||
		policy.Retention.Tokens == nil || *policy.Retention.Tokens != 400 ||
		policy.SummarizationProvider != "summary" ||
		policy.SummarizationModel != "summary-model" ||
		policy.MaxTokens != 2048 || policy.CompactionRetries != 4 ||
		policy.MaxOverflowRetries != 3 {
		t.Fatalf("merged policy = %#v", policy)
	}
	other := ResolveTargetPolicy(resolved, RouteTarget{
		Provider: "deepseek",
		Model:    "reasoner",
	})
	if other.Retention.Tokens == nil || *other.Retention.Tokens != 200 ||
		other.ThresholdRatio != defaultThresholdRatio {
		t.Fatalf("default route policy = %#v", other)
	}
}

func TestResolveCompactSpecUsesModelCapacity(t *testing.T) {
	t.Parallel()
	ratio := 0.125
	policy := ResolvedTargetPolicy{
		Target: RouteTarget{
			Provider: "deepseek",
			Model:    "chat",
		},
		ThresholdRatio:     0.8,
		Retention:          Retention{Ratio: &ratio},
		MaxTokens:          8192,
		CompactionRetries:  1,
		MaxOverflowRetries: 2,
	}
	spec, err := ResolveCompactSpec(policy, 1001)
	if err != nil {
		t.Fatal(err)
	}
	if spec.ContextWindow != 1001 || spec.ThresholdTokens != 800 ||
		spec.RetainTokens != 125 || spec.MaxOverflowRetries != 2 {
		t.Fatalf("compact spec = %#v", spec)
	}
	policy.Retention = Retention{Tokens: int64Pointer(800)}
	_, err = ResolveCompactSpec(policy, 1000)
	var targetProblem *TargetPressureConfigError
	if !errors.As(err, &targetProblem) || targetProblem.TargetKey != "deepseek/chat" {
		t.Fatalf("capacity error = %T %v", err, err)
	}
}

func TestResolveConfigRejectsInvalidPolicies(t *testing.T) {
	t.Parallel()
	provider := "summary"
	model := "model"
	tests := []struct {
		name     string
		settings Config
		contains string
	}{
		{
			name: "zero threshold",
			settings: Config{
				PolicyConfig: PolicyConfig{ThresholdRatio: floatPointer(0)},
			},
			contains: "thresholdRatio",
		},
		{
			name: "default retention exceeds threshold",
			settings: Config{
				PolicyConfig: PolicyConfig{ThresholdRatio: floatPointer(0.1)},
			},
			contains: "retainRatio",
		},
		{
			name: "two retention forms",
			settings: Config{
				PolicyConfig: PolicyConfig{
					RetainRatio:  floatPointer(0.2),
					RetainTokens: int64Pointer(10),
				},
			},
			contains: "mutually exclusive",
		},
		{
			name: "incomplete summary route",
			settings: Config{
				PolicyConfig: PolicyConfig{SummarizationProvider: &provider},
			},
			contains: "must be set together",
		},
		{
			name: "mismatched empty summary route",
			settings: Config{
				PolicyConfig: PolicyConfig{
					SummarizationProvider: new(string),
					SummarizationModel:    &model,
				},
			},
			contains: "empty or non-empty pair",
		},
		{
			name: "negative retries",
			settings: Config{
				PolicyConfig: PolicyConfig{CompactionRetries: intPointer(-1)},
			},
			contains: "compactionRetries",
		},
		{
			name: "duplicate exact route",
			settings: Config{
				ModelPolicies: []ModelPolicyConfig{
					{Provider: "p", Model: "m"},
					{Provider: "p", Model: "m"},
				},
			},
			contains: "duplicate model policy",
		},
		{
			name: "override inherits invalid retention",
			settings: Config{
				ModelPolicies: []ModelPolicyConfig{
					{
						Provider: "p",
						Model:    "m",
						PolicyConfig: PolicyConfig{
							ThresholdRatio: floatPointer(0.1),
						},
					},
				},
			},
			contains: "retainRatio",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			_, err := ResolveConfig(testCase.settings)
			if err == nil || !strings.Contains(err.Error(), testCase.contains) {
				t.Fatalf("ResolveConfig error = %v, want %q", err, testCase.contains)
			}
		})
	}
}

func TestCloneResolvedConfigPreservesNilShape(t *testing.T) {
	t.Parallel()
	source := ResolvedConfig{
		ThresholdRatio:     0.8,
		Retention:          Retention{Tokens: int64Pointer(50)},
		ModelPolicies:      []ModelPolicyConfig(nil),
		MaxTokens:          100,
		CompactionRetries:  1,
		MaxOverflowRetries: 1,
		Auto:               true,
	}
	detached := cloneResolvedConfig(source)
	if !reflect.DeepEqual(source, detached) {
		t.Fatalf("clone = %#v, source = %#v", detached, source)
	}
	*detached.Retention.Tokens = 99
	if *source.Retention.Tokens != 50 {
		t.Fatal("clone aliases retention")
	}
}

func intPointer(value int) *int {
	return &value
}
