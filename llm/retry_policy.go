package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
)

const (
	DefaultMaxRetries        int64   = 2
	DefaultInitialDelayMS    float64 = 500
	DefaultMaxDelayMS        float64 = 10_000
	DefaultJitterRatio       float64 = 0.1
	MaxTimerDelayMS          float64 = 2_147_483_647
	maxJavaScriptSafeInteger int64   = 9_007_199_254_740_991
)

var defaultRetryableCodes = []string{
	EmptyResponseCode,
	"RATE_LIMIT",
	"SERVER",
	"TIMEOUT",
	"TRANSPORT",
}

// RetryMode selects bounded transient retries or unbounded retry-until-cancelled behavior.
type RetryMode string

const (
	RetryNormal RetryMode = "normal"
	RetryAlways RetryMode = "always"
)

// BackoffConfig contains optional provider-owned backoff overrides in milliseconds.
type BackoffConfig struct {
	InitialDelayMS *float64 `json:"initialDelayMs,omitempty"`
	MaxDelayMS     *float64 `json:"maxDelayMs,omitempty"`
	JitterRatio    *float64 `json:"jitterRatio,omitempty"`
}

// RetryPolicyConfig is the strict tagged Go representation of the source policy union.
type RetryPolicyConfig struct {
	Mode           RetryMode      `json:"mode"`
	MaxRetries     *int64         `json:"maxRetries,omitempty"`
	RetryableCodes []string       `json:"retryableCodes,omitempty"`
	Backoff        *BackoffConfig `json:"backoff,omitempty"`
}

// ResolvedRetryBackoff is a fully materialized bounded delay policy.
type ResolvedRetryBackoff struct {
	InitialDelayMS float64 `json:"initialDelayMs"`
	MaxDelayMS     float64 `json:"maxDelayMs"`
	JitterRatio    float64 `json:"jitterRatio"`
}

// RetryPolicy is one detached provider policy captured with its adapter registration.
type RetryPolicy interface {
	RetryMode() RetryMode
	RetryBackoff() ResolvedRetryBackoff
	CloneRetryPolicy() RetryPolicy
}

// NormalRetryPolicy is bounded and restricted to configured transient codes.
type NormalRetryPolicy struct {
	ResolvedRetryBackoff
	Mode           RetryMode `json:"mode"`
	MaxRetries     int64     `json:"maxRetries"`
	RetryableCodes []string  `json:"retryableCodes"`
}

func (NormalRetryPolicy) RetryMode() RetryMode { return RetryNormal }
func (policy NormalRetryPolicy) RetryBackoff() ResolvedRetryBackoff {
	return policy.ResolvedRetryBackoff
}
func (policy NormalRetryPolicy) CloneRetryPolicy() RetryPolicy {
	policy.Mode = RetryNormal
	policy.RetryableCodes = slices.Clone(policy.RetryableCodes)
	return policy
}

// AlwaysRetryPolicy retries every model failure until success, cancellation, or disposal.
type AlwaysRetryPolicy struct {
	ResolvedRetryBackoff
	Mode RetryMode `json:"mode"`
}

func (AlwaysRetryPolicy) RetryMode() RetryMode { return RetryAlways }
func (policy AlwaysRetryPolicy) RetryBackoff() ResolvedRetryBackoff {
	return policy.ResolvedRetryBackoff
}
func (policy AlwaysRetryPolicy) CloneRetryPolicy() RetryPolicy {
	policy.Mode = RetryAlways
	return policy
}

// ResolveRetryPolicy validates, defaults, and detaches one provider-owned policy.
func ResolveRetryPolicy(settings *RetryPolicyConfig, diagnosticPath string) (RetryPolicy, error) {
	if diagnosticPath == "" {
		diagnosticPath = "llm retryPolicy"
	}
	if settings == nil {
		resolvedBackoff, err := resolveRetryBackoff(nil, diagnosticPath+".backoff")
		if err != nil {
			return nil, err
		}
		return NormalRetryPolicy{
			ResolvedRetryBackoff: resolvedBackoff, Mode: RetryNormal,
			MaxRetries: DefaultMaxRetries, RetryableCodes: slices.Clone(defaultRetryableCodes),
		}, nil
	}
	resolvedBackoff, err := resolveRetryBackoff(settings.Backoff, diagnosticPath+".backoff")
	if err != nil {
		return nil, err
	}
	switch settings.Mode {
	case RetryNormal:
		retryLimit := DefaultMaxRetries
		if settings.MaxRetries != nil {
			retryLimit = *settings.MaxRetries
		}
		if retryLimit < 0 || retryLimit > maxJavaScriptSafeInteger {
			return nil, fmt.Errorf("%s.maxRetries must be a non-negative safe integer", diagnosticPath)
		}
		codes := slices.Clone(settings.RetryableCodes)
		if codes == nil {
			codes = slices.Clone(defaultRetryableCodes)
		}
		if err := validateRetryableCodes(codes, diagnosticPath); err != nil {
			return nil, err
		}
		return NormalRetryPolicy{
			ResolvedRetryBackoff: resolvedBackoff, Mode: RetryNormal,
			MaxRetries: retryLimit, RetryableCodes: codes,
		}, nil
	case RetryAlways:
		if settings.MaxRetries != nil || settings.RetryableCodes != nil {
			return nil, fmt.Errorf("%s: maxRetries and retryableCodes are valid only in normal mode", diagnosticPath)
		}
		return AlwaysRetryPolicy{ResolvedRetryBackoff: resolvedBackoff, Mode: RetryAlways}, nil
	default:
		return nil, fmt.Errorf("%s.mode must be normal or always", diagnosticPath)
	}
}

func resolveRetryBackoff(settings *BackoffConfig, diagnosticPath string) (ResolvedRetryBackoff, error) {
	initialDelay := DefaultInitialDelayMS
	maximumDelay := DefaultMaxDelayMS
	jitter := DefaultJitterRatio
	if settings != nil {
		if settings.InitialDelayMS != nil {
			initialDelay = *settings.InitialDelayMS
		}
		if settings.MaxDelayMS != nil {
			maximumDelay = *settings.MaxDelayMS
		}
		if settings.JitterRatio != nil {
			jitter = *settings.JitterRatio
		}
	}
	if !finitePositive(initialDelay) || initialDelay > MaxTimerDelayMS {
		return ResolvedRetryBackoff{}, fmt.Errorf("%s.initialDelayMs must be positive and no greater than %.0f", diagnosticPath, MaxTimerDelayMS)
	}
	if !finitePositive(maximumDelay) || maximumDelay > MaxTimerDelayMS {
		return ResolvedRetryBackoff{}, fmt.Errorf("%s.maxDelayMs must be positive and no greater than %.0f", diagnosticPath, MaxTimerDelayMS)
	}
	if initialDelay > maximumDelay {
		return ResolvedRetryBackoff{}, fmt.Errorf("%s.initialDelayMs must be less than or equal to maxDelayMs", diagnosticPath)
	}
	if math.IsNaN(jitter) || math.IsInf(jitter, 0) || jitter < 0 || jitter > 1 {
		return ResolvedRetryBackoff{}, fmt.Errorf("%s.jitterRatio must be between 0 and 1", diagnosticPath)
	}
	return ResolvedRetryBackoff{InitialDelayMS: initialDelay, MaxDelayMS: maximumDelay, JitterRatio: jitter}, nil
}

func finitePositive(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value > 0
}

func validateRetryableCodes(codes []string, diagnosticPath string) error {
	if len(codes) == 0 {
		return fmt.Errorf("%s.retryableCodes must not be empty", diagnosticPath)
	}
	seen := make(map[string]struct{}, len(codes))
	for _, failureCode := range codes {
		if failureCode == "" {
			return fmt.Errorf("%s.retryableCodes must contain only non-empty strings", diagnosticPath)
		}
		if _, exists := seen[failureCode]; exists {
			return fmt.Errorf("%s.retryableCodes must not contain duplicates", diagnosticPath)
		}
		seen[failureCode] = struct{}{}
	}
	return nil
}

type retryPolicyWire struct {
	Mode           json.RawMessage `json:"mode"`
	MaxRetries     json.RawMessage `json:"maxRetries"`
	RetryableCodes json.RawMessage `json:"retryableCodes"`
	Backoff        json.RawMessage `json:"backoff"`
}

// UnmarshalJSON preserves omission and enforces the source tagged-union keys.
func (settings *RetryPolicyConfig) UnmarshalJSON(encoded []byte) error {
	if settings == nil {
		return errors.New("llm: cannot decode retryPolicy into nil target")
	}
	if bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) {
		return errors.New("llm: retryPolicy must be an object")
	}
	var wireValue retryPolicyWire
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wireValue); err != nil {
		return err
	}
	var decoded RetryPolicyConfig
	if len(wireValue.Mode) == 0 || bytes.Equal(bytes.TrimSpace(wireValue.Mode), []byte("null")) {
		return errors.New("llm: retryPolicy.mode must be normal or always")
	}
	if err := json.Unmarshal(wireValue.Mode, &decoded.Mode); err != nil {
		return errors.New("llm: retryPolicy.mode must be normal or always")
	}
	if len(wireValue.MaxRetries) != 0 {
		if bytes.Equal(bytes.TrimSpace(wireValue.MaxRetries), []byte("null")) {
			return errors.New("llm: retryPolicy.maxRetries must be a non-negative safe integer")
		}
		var retryLimit int64
		if err := json.Unmarshal(wireValue.MaxRetries, &retryLimit); err != nil {
			return errors.New("llm: retryPolicy.maxRetries must be a non-negative safe integer")
		}
		decoded.MaxRetries = &retryLimit
	}
	if len(wireValue.RetryableCodes) != 0 {
		if bytes.Equal(bytes.TrimSpace(wireValue.RetryableCodes), []byte("null")) {
			return errors.New("llm: retryPolicy.retryableCodes must be an array")
		}
		if err := json.Unmarshal(wireValue.RetryableCodes, &decoded.RetryableCodes); err != nil {
			return errors.New("llm: retryPolicy.retryableCodes must be an array")
		}
	}
	if len(wireValue.Backoff) != 0 {
		if bytes.Equal(bytes.TrimSpace(wireValue.Backoff), []byte("null")) {
			return errors.New("llm: retryPolicy.backoff must be an object")
		}
		var backoffSettings BackoffConfig
		decoder = json.NewDecoder(bytes.NewReader(wireValue.Backoff))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&backoffSettings); err != nil {
			return err
		}
		decoded.Backoff = &backoffSettings
	}
	*settings = decoded
	return nil
}
