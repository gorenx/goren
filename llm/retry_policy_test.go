package llm_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/gorenx/goren/llm"
)

func TestResolveRetryPolicyDefaultsAndDetaches(t *testing.T) {
	t.Parallel()
	policy, err := llm.ResolveRetryPolicy(nil, "provider.retryPolicy")
	if err != nil {
		t.Fatal(err)
	}
	normal := policy.(llm.NormalRetryPolicy)
	wantCodes := []string{"EMPTY_RESPONSE", "RATE_LIMIT", "SERVER", "TIMEOUT", "TRANSPORT"}
	if normal.MaxRetries != 2 || !reflect.DeepEqual(normal.RetryableCodes, wantCodes) ||
		normal.InitialDelayMS != 500 || normal.MaxDelayMS != 10_000 || normal.JitterRatio != 0.1 {
		t.Fatalf("default policy = %#v", normal)
	}
	codes := []string{"BUSY"}
	retryLimit := int64(4)
	initialDelay := 25.0
	maximumDelay := 100.0
	zeroJitter := 0.0
	configured, err := llm.ResolveRetryPolicy(&llm.RetryPolicyConfig{
		Mode: llm.RetryNormal, MaxRetries: &retryLimit, RetryableCodes: codes,
		Backoff: &llm.BackoffConfig{InitialDelayMS: &initialDelay, MaxDelayMS: &maximumDelay, JitterRatio: &zeroJitter},
	}, "provider.retryPolicy")
	if err != nil {
		t.Fatal(err)
	}
	codes[0] = "LATE"
	configuredNormal := configured.(llm.NormalRetryPolicy)
	if !reflect.DeepEqual(configuredNormal.RetryableCodes, []string{"BUSY"}) {
		t.Fatalf("configured codes = %#v", configuredNormal.RetryableCodes)
	}
}

func TestRetryPolicyStrictTaggedConfig(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		input       string
		wantMessage string
	}{
		{input: `{"mode":"normal","retryableCodes":[]}`, wantMessage: "must not be empty"},
		{input: `{"mode":"normal","maxRetires":1}`, wantMessage: "unknown field"},
		{input: `{"mode":"always","maxRetries":1}`, wantMessage: "valid only in normal mode"},
		{input: `{"mode":"always","backoff":{"initialDelay":1}}`, wantMessage: "unknown field"},
		{input: `{"mode":"sometimes"}`, wantMessage: "mode must be normal or always"},
	} {
		testCase := testCase
		t.Run(testCase.input, func(t *testing.T) {
			t.Parallel()
			var settings llm.RetryPolicyConfig
			err := json.Unmarshal([]byte(testCase.input), &settings)
			if err == nil {
				_, err = llm.ResolveRetryPolicy(&settings, "provider.retryPolicy")
			}
			if err == nil || !strings.Contains(err.Error(), testCase.wantMessage) {
				t.Fatalf("error = %v, want containing %q", err, testCase.wantMessage)
			}
		})
	}
}
