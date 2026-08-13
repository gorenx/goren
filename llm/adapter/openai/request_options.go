package openai

import (
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/gorenx/goren/llm"
	"github.com/openai/openai-go/v3/option"
)

func transportRequestOptions(
	targetModel llm.Model,
	compatibleBehavior Compatibility,
	invocationOptions llm.StreamOptions,
	httpResponse **http.Response,
) []option.RequestOption {
	opts := make([]option.RequestOption, 0, len(targetModel.Headers)+len(invocationOptions.Headers)+8)
	opts = append(opts, option.WithAPIKey(invocationOptions.APIKey))
	for name, value := range targetModel.Headers {
		opts = append(opts, option.WithHeader(name, value))
	}
	for name, value := range invocationOptions.Headers {
		opts = append(opts, option.WithHeader(name, value))
	}
	if invocationOptions.MaxRetries != nil {
		opts = append(opts, option.WithMaxRetries(*invocationOptions.MaxRetries))
	}
	if invocationOptions.Timeout > 0 {
		opts = append(opts, option.WithRequestTimeout(invocationOptions.Timeout))
	}
	if httpResponse != nil {
		opts = append(opts, option.WithResponseInto(httpResponse))
	}
	if invocationOptions.RequestID != "" {
		opts = append(opts, option.WithHeader("x-client-request-id", invocationOptions.RequestID))
	}
	if invocationOptions.SessionID != "" && (invocationOptions.CacheKey != "" || invocationOptions.CacheRetention != "") {
		for _, headerName := range compatibleBehavior.SessionAffinityHeaders {
			opts = append(opts, option.WithHeader(headerName, invocationOptions.SessionID))
		}
	}
	if invocationOptions.MaxRetryDelay > 0 {
		opts = append(opts, option.WithMiddleware(capRetryDelay(invocationOptions.MaxRetryDelay)))
	}
	if invocationOptions.ThinkingBudget > 0 && compatibleBehavior.ThinkingBudgetField != "" {
		opts = append(opts, option.WithJSONSet(compatibleBehavior.ThinkingBudgetField, invocationOptions.ThinkingBudget))
	}
	return opts
}

func capRetryDelay(maximum time.Duration) option.Middleware {
	return func(request *http.Request, next option.MiddlewareNext) (*http.Response, error) {
		response, err := next(request)
		if response == nil || maximum <= 0 {
			return response, err
		}
		delay, ok := responseRetryDelay(response)
		if !ok {
			retryCount, _ := strconv.Atoi(request.Header.Get("X-Stainless-Retry-Count"))
			delay = min(8*time.Second, time.Duration(0.5*float64(time.Second)*math.Pow(2, float64(retryCount))))
		}
		delay = min(delay, maximum)
		milliseconds := max(int64(0), delay.Milliseconds())
		response.Header.Set("Retry-After-Ms", strconv.FormatInt(milliseconds, 10))
		response.Header.Del("Retry-After")
		return response, err
	}
}

func responseRetryDelay(response *http.Response) (time.Duration, bool) {
	if milliseconds := response.Header.Get("Retry-After-Ms"); milliseconds != "" {
		value, err := strconv.ParseFloat(milliseconds, 64)
		if err == nil {
			return max(0, time.Duration(value*float64(time.Millisecond))), true
		}
	}
	value := response.Header.Get("Retry-After")
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil {
		return max(0, time.Duration(seconds*float64(time.Second))), true
	}
	retryAt, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	return max(0, time.Until(retryAt)), true
}
