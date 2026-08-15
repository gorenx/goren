package llmretry

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/gorenx/goren/llm"
)

func policyKey(resolved llm.RetryPolicy) (string, error) {
	delayPolicy := resolved.RetryBackoff()
	initialValue, err := json.Marshal(delayPolicy.InitialDelayMS)
	if err != nil {
		return "", err
	}
	maximumValue, err := json.Marshal(delayPolicy.MaxDelayMS)
	if err != nil {
		return "", err
	}
	jitterValue, err := json.Marshal(delayPolicy.JitterRatio)
	if err != nil {
		return "", err
	}
	switch configured := resolved.(type) {
	case llm.NormalRetryPolicy:
		codes := slices.Clone(configured.RetryableCodes)
		slices.Sort(codes)
		codesValue, marshalErr := json.Marshal(codes)
		if marshalErr != nil {
			return "", marshalErr
		}
		return strings.Join([]string{
			`["normal"`, strconv.FormatInt(configured.MaxRetries, 10), string(codesValue),
			string(initialValue), string(maximumValue), string(jitterValue) + "]",
		}, ","), nil
	case llm.AlwaysRetryPolicy:
		return strings.Join([]string{
			`["always"`, string(initialValue), string(maximumValue), string(jitterValue) + "]",
		}, ","), nil
	default:
		return "", errors.New("llm-retry: unsupported retry policy implementation")
	}
}

func localDelay(resolved llm.RetryPolicy, retryNumber int64, randomSample func() float64) (float64, error) {
	if retryNumber < 1 {
		return 0, errors.New("llm-retry: retry number must be positive")
	}
	sample := randomSample()
	if math.IsNaN(sample) || math.IsInf(sample, 0) || sample < 0 || sample > 1 {
		return 0, fmt.Errorf("llm-retry: random sample %v is outside 0..1", sample)
	}
	delayPolicy := resolved.RetryBackoff()
	exponent := retryNumber - 1
	if exponent > 1024 {
		exponent = 1024
	}
	exponential := math.Min(delayPolicy.InitialDelayMS*math.Pow(2, float64(exponent)), delayPolicy.MaxDelayMS)
	jitter := 1 - delayPolicy.JitterRatio + 2*delayPolicy.JitterRatio*sample
	return math.Min(exponential*jitter, delayPolicy.MaxDelayMS), nil
}

func retryable(configured llm.NormalRetryPolicy, failureCode string) bool {
	return slices.Contains(configured.RetryableCodes, failureCode)
}
