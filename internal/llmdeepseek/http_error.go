package llmdeepseek

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gorenx/goren/llm"
)

var (
	contextStructuredPattern = regexp.MustCompile(`(?i)(^|[^a-z0-9])context[\s_-](length|window)[\s_-](exceed(ed|s)?|overflow(ed)?|limit[\s_-]exceeded)($|[^a-z0-9])`)
	contextMaximumPattern    = regexp.MustCompile(`(?i)\b(maximum|max)(\s+(allowed|supported))?\s+context\s+(length|window)\b`)
	contextTooLargePattern   = regexp.MustCompile(`(?i)\b(request|prompt|input|messages?)\s+(is\s+|are\s+)?too\s+(large|long)\s+for\s+((this|the)\s+)?(model('s)?\s+)?context(\s+window)?\b`)
	modelTooLargePattern     = regexp.MustCompile(`(?i)\b(input|prompt|request)\s+(is\s+)?too\s+(long|large)\s+for\s+(this|the)\s+model\b`)
	contextExceedsPattern    = regexp.MustCompile(`(?i)\b(input|prompt|request|messages?)\b.{0,40}\b(exceeds?|exceeded|overflows?|is\s+larger\s+than)\b.{0,40}\b(the\s+)?(model('s)?\s+)?context(\s+(length|window))?\b`)
	quotaInsufficientPattern = regexp.MustCompile(`(?i)\binsufficient[\s_-]+(quota|balance|credits?)\b`)
	quotaExceededPattern     = regexp.MustCompile(`(?i)\b(quota|usage[\s_-]+limit)[\s_-]+(exceeded|exhausted|reached)\b`)
	quotaCurrentPattern      = regexp.MustCompile(`(?i)\bexceed(ed|s)?[\s_-]+((your|the)[\s_-]+)?(current[\s_-]+)?quota\b`)
	quotaBalancePattern      = regexp.MustCompile(`(?i)\b(balance|credits?)[\s_-]+(exhausted|depleted)\b|\bout[\s_-]+of[\s_-]+(credits?|budget)\b`)
)

func (backend *Adapter) httpFailure(response *http.Response) error {
	if response.Body != nil {
		defer response.Body.Close()
	}
	message := fmt.Sprintf("DeepSeek API error (HTTP %d)", response.StatusCode)
	var providerError *wireErrorDetail
	if response.Body != nil {
		var body wireErrorBody
		if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&body); err == nil {
			providerError = body.Error
			if providerError != nil && providerError.Message != "" {
				message = providerError.Message
			}
		}
	}
	retryAfter := providerRetryAfterMS(response.Header.Get("retry-after"), backend.now())
	requestID := response.Header.Get("x-request-id")
	if requestID == "" {
		requestID = response.Header.Get("x-deepseek-request-id")
	}
	status := response.StatusCode
	return llm.MustLlmError(message, HTTPErrorCode(status, providerError), llm.LlmErrorOptions{
		Status: &status, ProviderRetryAfterMS: retryAfter, RequestID: llm.ProviderRequestID(requestID),
	})
}

func providerRetryAfterMS(rawValue string, now time.Time) *float64 {
	if rawValue == "" {
		return nil
	}
	if onlyDigits(rawValue) {
		seconds, err := strconv.ParseFloat(rawValue, 64)
		delay := seconds * 1_000
		if err != nil || delay <= 0 || math.IsNaN(delay) || math.IsInf(delay, 0) {
			return nil
		}
		return &delay
	}
	deadline, err := http.ParseTime(rawValue)
	if err != nil {
		return nil
	}
	delay := float64(deadline.Sub(now).Milliseconds())
	if delay <= 0 {
		return nil
	}
	return &delay
}

func onlyDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

// HTTPErrorCode maps provider status and structured details to stable Harness codes.
func HTTPErrorCode(status int, providerError *wireErrorDetail) string {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return "AUTH"
	}
	detail := ""
	if providerError != nil {
		detail = strings.Join(nonEmpty(providerError.Code, providerError.Type, providerError.Message), " ")
	}
	if isQuotaError(detail) {
		return llm.QuotaExceededCode
	}
	if status == http.StatusTooManyRequests {
		return "RATE_LIMIT"
	}
	if status == http.StatusBadRequest {
		if isContextWindowError(detail) {
			return llm.ContextWindowExceededCode
		}
		return "INVALID_REQUEST"
	}
	if status >= http.StatusInternalServerError {
		return "SERVER"
	}
	return fmt.Sprintf("HTTP_%d", status)
}

func nonEmpty(values ...string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

func isContextWindowError(detail string) bool {
	if contextStructuredPattern.MatchString(detail) || contextMaximumPattern.MatchString(detail) ||
		contextTooLargePattern.MatchString(detail) || modelTooLargePattern.MatchString(detail) ||
		contextExceedsPattern.MatchString(detail) {
		return true
	}
	lower := strings.ToLower(detail)
	return strings.Contains(lower, "context_length_exceeded") || strings.Contains(lower, "context-window-exceeded")
}

func isQuotaError(detail string) bool {
	return quotaInsufficientPattern.MatchString(detail) || quotaExceededPattern.MatchString(detail) ||
		quotaCurrentPattern.MatchString(detail) || quotaBalancePattern.MatchString(detail)
}
