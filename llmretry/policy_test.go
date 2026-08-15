package llmretry

import (
	"testing"

	"github.com/gorenx/goren/llm"
)

func TestPolicyKeyAndJitteredExponentialDelay(t *testing.T) {
	t.Parallel()
	resolved := llm.NormalRetryPolicy{
		ResolvedRetryBackoff: llm.ResolvedRetryBackoff{
			InitialDelayMS: 500, MaxDelayMS: 2_000, JitterRatio: 0.1,
		},
		Mode: llm.RetryNormal, MaxRetries: 3, RetryableCodes: []string{"SERVER", "RATE_LIMIT"},
	}
	identity, err := policyKey(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if identity != `["normal",3,["RATE_LIMIT","SERVER"],500,2000,0.1]` {
		t.Fatalf("policy key = %q", identity)
	}

	samples := []float64{0, 1, 0.5, 0.5}
	randomSample := func() float64 {
		value := samples[0]
		samples = samples[1:]
		return value
	}
	want := []float64{450, 1_100, 2_000, 2_000}
	for retryNumber, wantDelay := range want {
		got, delayErr := localDelay(resolved, int64(retryNumber+1), randomSample)
		if delayErr != nil {
			t.Fatal(delayErr)
		}
		if got != wantDelay {
			t.Fatalf("retry %d delay = %v, want %v", retryNumber+1, got, wantDelay)
		}
	}
}

func TestAlwaysPolicyKeyOmitsNormalBudget(t *testing.T) {
	t.Parallel()
	resolved := llm.AlwaysRetryPolicy{
		ResolvedRetryBackoff: llm.ResolvedRetryBackoff{
			InitialDelayMS: 2, MaxDelayMS: 4, JitterRatio: 0.5,
		},
		Mode: llm.RetryAlways,
	}
	identity, err := policyKey(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if identity != `["always",2,4,0.5]` {
		t.Fatalf("policy key = %q", identity)
	}
}
