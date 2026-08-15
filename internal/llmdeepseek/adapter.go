package llmdeepseek

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorenx/goren/llm"
)

const (
	DefaultHarnessVersion = "0.1.0-rc.5"
	productName           = "deepseek-harness"
	productURL            = "https://github.com/deepseek-ai/deepseek-harness"
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

// RequestSender is the outbound HTTP capability consumed by the DeepSeek adapter.
type RequestSender interface {
	Do(*http.Request) (*http.Response, error)
}

// AdapterOptions supplies operation-local configuration and identity resolvers.
type AdapterOptions struct {
	CurrentOptions func() (ConnectionOptions, error)
	ResolveAPIKey  func(context.Context, ConnectionOptions) (string, error)
	ResolveUserID  func() (string, error)
	RequestSender  RequestSender
	Version        string
	Now            func() time.Time
}

// Adapter is the direct DeepSeek chat-completions outbound adapter.
type Adapter struct {
	currentOptions func() (ConnectionOptions, error)
	resolveAPIKey  func(context.Context, ConnectionOptions) (string, error)
	resolveUserID  func() (string, error)
	requestSender  RequestSender
	version        string
	now            func() time.Time
}

// NewAdapter constructs a transport adapter from consumer-owned resolution seams.
func NewAdapter(settings AdapterOptions) (*Adapter, error) {
	if settings.CurrentOptions == nil {
		return nil, errors.New("llm-deepseek: current options resolver is nil")
	}
	if settings.ResolveAPIKey == nil {
		return nil, errors.New("llm-deepseek: API key resolver is nil")
	}
	if settings.ResolveUserID == nil {
		return nil, errors.New("llm-deepseek: anonymous user id resolver is nil")
	}
	transport := settings.RequestSender
	if transport == nil {
		transport = http.DefaultClient
	}
	version := settings.Version
	if version == "" {
		version = DefaultHarnessVersion
	}
	now := settings.Now
	if now == nil {
		now = time.Now
	}
	return &Adapter{
		currentOptions: settings.CurrentOptions, resolveAPIKey: settings.ResolveAPIKey,
		resolveUserID: settings.ResolveUserID, requestSender: transport,
		version: version, now: now,
	}, nil
}

func (backend *Adapter) DescribeProvider(providerRoute string) (llm.ProviderInfo, error) {
	return llm.ProviderInfo{ID: providerRoute, Name: "DeepSeek"}, nil
}

func (backend *Adapter) ProviderRetryPolicy(string) (llm.RetryPolicy, error) {
	connection, err := backend.currentOptions()
	if err != nil {
		return nil, err
	}
	if connection.RetryPolicy == nil {
		return nil, errors.New("llm-deepseek: resolved retry policy is nil")
	}
	return connection.RetryPolicy.CloneRetryPolicy(), nil
}

func (backend *Adapter) ListModels(requestContext context.Context, providerRoute string) ([]llm.ModelInfo, error) {
	if err := requestContext.Err(); err != nil {
		return nil, err
	}
	connection, err := backend.currentOptions()
	if err != nil {
		return nil, err
	}
	models := make([]llm.ModelInfo, 0, len(connection.Models))
	for _, catalogEntry := range connection.Models {
		models = append(models, modelInfo(providerRoute, catalogEntry))
	}
	return models, nil
}

func (backend *Adapter) ResolveModel(requestContext context.Context, providerRoute string, modelID string) (llm.ResolvedModelInfo, error) {
	if err := requestContext.Err(); err != nil {
		return llm.ResolvedModelInfo{}, err
	}
	connection, err := backend.currentOptions()
	if err != nil {
		return llm.ResolvedModelInfo{}, err
	}
	var configured *CatalogModel
	for index := range connection.Models {
		if connection.Models[index].ID == modelID {
			entry := connection.Models[index]
			configured = &entry
			break
		}
	}
	resolved := llm.ResolvedModelInfo{}
	contextWindow := connection.DefaultContextWindow
	maximumTokens := connection.MaxTokens
	if configured == nil {
		resolved.ModelInfo = llm.ModelInfo{
			Provider: providerRoute, ID: modelID, Name: modelID,
			InputModalities: []llm.ModelModality{llm.ModalityText},
		}
	} else {
		resolved.ModelInfo = modelInfo(providerRoute, *configured)
		if configured.ContextWindow != nil {
			contextWindow = *configured.ContextWindow
		}
		if configured.MaxTokens != nil {
			maximumTokens = *configured.MaxTokens
		}
	}
	resolved.Context = &llm.ModelContext{ContextWindow: contextWindow}
	resolved.DefaultMaxTokens = intPointer(maximumTokens)
	resolved.Reasoning = reasoningInfo(connection.Defaults)
	return resolved, nil
}

func modelInfo(providerRoute string, catalogEntry CatalogModel) llm.ModelInfo {
	modelName := catalogEntry.ID
	if catalogEntry.Name != nil {
		modelName = *catalogEntry.Name
	}
	description := ""
	if catalogEntry.Description != nil {
		description = *catalogEntry.Description
	}
	return llm.ModelInfo{
		Provider: providerRoute, ID: catalogEntry.ID, Name: modelName, Description: description,
		InputModalities: []llm.ModelModality{llm.ModalityText},
	}
}

func reasoningInfo(defaults RequestDefaults) *llm.ModelReasoningInfo {
	if defaults.Thinking != nil && *defaults.Thinking == ThinkingDisabled {
		return &llm.ModelReasoningInfo{
			Efforts:       []llm.ReasoningEffortInfo{{ID: llm.ReasoningEffortID(ReasoningOff), Name: "Off"}},
			DefaultEffort: llm.ReasoningEffortID(ReasoningOff),
		}
	}
	defaultEffort := llm.ReasoningEffortID(ReasoningHigh)
	if defaults.ReasoningEffort != nil {
		defaultEffort = llm.ReasoningEffortID(*defaults.ReasoningEffort)
	}
	return &llm.ModelReasoningInfo{
		Efforts: []llm.ReasoningEffortInfo{
			{ID: llm.ReasoningEffortID(ReasoningOff), Name: "Off"},
			{ID: llm.ReasoningEffortID(ReasoningHigh), Name: "High"},
			{ID: llm.ReasoningEffortID(ReasoningMax), Name: "Max"},
		},
		DefaultEffort: defaultEffort,
	}
}

// Stream creates a lazy pull stream so configuration and credentials are
// resolved exactly once when the request actually starts.
func (backend *Adapter) Stream(requestContext context.Context, requestOptions llm.GenerateOptions) (llm.ChunkStream, error) {
	if requestContext == nil {
		requestContext = context.Background()
	}
	operationContext, cancelOperation := context.WithCancel(requestContext)
	return &deepSeekStream{
		backend: backend, requestOptions: requestOptions,
		operationContext: operationContext, cancelOperation: cancelOperation,
	}, nil
}

type deepSeekStream struct {
	backend        *Adapter
	requestOptions llm.GenerateOptions

	operationContext context.Context
	cancelOperation  context.CancelFunc

	nextMu      sync.Mutex
	mu          sync.Mutex
	initialized bool
	closed      bool
	terminated  bool
	downstream  llm.ChunkStream
	baseURL     string
}

func (streamState *deepSeekStream) Next(requestContext context.Context) (llm.StreamChunk, bool, error) {
	if requestContext == nil {
		requestContext = context.Background()
	}
	streamState.nextMu.Lock()
	defer streamState.nextMu.Unlock()
	if err := requestContext.Err(); err != nil {
		streamState.cancelOperation()
		return nil, false, abortedError(err)
	}
	streamState.mu.Lock()
	if streamState.closed {
		streamState.mu.Unlock()
		return nil, false, nil
	}
	initialized := streamState.initialized
	terminated := streamState.terminated
	streamState.mu.Unlock()
	if terminated {
		return nil, false, nil
	}
	if !initialized {
		if err := streamState.initialize(requestContext); err != nil {
			streamState.markTerminated()
			streamState.cancelOperation()
			return nil, false, err
		}
	}
	entry, available, err := streamState.downstream.Next(requestContext)
	if err == nil {
		if !available || (entry != nil && entry.ChunkType() == "finish") {
			streamState.markTerminated()
			streamState.cancelOperation()
		}
		return entry, available, nil
	}
	normalized := streamState.normalizeStreamError(requestContext, err)
	streamState.markTerminated()
	streamState.cancelOperation()
	return nil, false, normalized
}

func (streamState *deepSeekStream) initialize(requestContext context.Context) error {
	connection, err := streamState.backend.currentOptions()
	if err != nil {
		return err
	}
	apiKey, err := streamState.backend.resolveAPIKey(requestContext, connection)
	if err != nil {
		return err
	}
	apiKey, err = normalizeAPIKey(apiKey, connection.APIKeyEnv)
	if err != nil {
		return err
	}
	userID, err := streamState.backend.resolveUserID()
	if err != nil {
		return err
	}
	if userID == "" {
		return errors.New("llm-deepseek: anonymous user id is empty")
	}
	wireValue, err := SerializeRequest(streamState.requestOptions, connection.Defaults)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(wireValue)
	if err != nil {
		return fmt.Errorf("llm-deepseek: encode request: %w", err)
	}
	endpoint := connection.BaseURL + "/chat/completions"
	httpRequest, err := http.NewRequestWithContext(
		streamState.operationContext, http.MethodPost, endpoint, bytes.NewReader(payload),
	)
	if err != nil {
		return err
	}
	httpRequest.Header.Set("authorization", "Bearer "+apiKey)
	httpRequest.Header.Set("content-type", "application/json")
	httpRequest.Header.Set("accept", "text/event-stream")
	httpRequest.Header.Set("user-agent", userAgent(streamState.backend.version))
	httpRequest.Header.Set("x-deepseek-harness-user-id", userID)
	if streamState.requestOptions.SessionID != "" {
		httpRequest.Header.Set("x-deepseek-harness-session-id", streamState.requestOptions.SessionID)
	}
	if streamState.requestOptions.Purpose == llm.PurposeCompaction {
		httpRequest.Header.Set("x-deepseek-harness-compact", "1")
	}

	response, err := streamState.doRequest(requestContext, httpRequest, connection.BaseURL, connection.StreamIdleTimeout)
	if err != nil {
		return err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return streamState.httpFailure(response)
	}
	if response.Body == nil || response.Body == http.NoBody {
		return llm.MustLlmError("DeepSeek API returned no response body", llm.EmptyResponseCode)
	}
	payloads, err := newSSEPayloadStream(response.Body, connection.StreamIdleTimeout, nil)
	if err != nil {
		_ = response.Body.Close()
		return err
	}
	downstream, err := translatePayloads(payloads)
	if err != nil {
		_ = payloads.Close(context.Background())
		return err
	}
	streamState.mu.Lock()
	streamState.initialized = true
	streamState.downstream = downstream
	streamState.baseURL = connection.BaseURL
	streamState.mu.Unlock()
	return nil
}

type responseResult struct {
	response *http.Response
	err      error
}

func (streamState *deepSeekStream) doRequest(
	requestContext context.Context,
	httpRequest *http.Request,
	baseURL string,
	idleTimeout time.Duration,
) (*http.Response, error) {
	resultChannel := make(chan responseResult)
	go func() {
		response, err := streamState.backend.requestSender.Do(httpRequest)
		result := responseResult{response: response, err: err}
		select {
		case resultChannel <- result:
		case <-streamState.operationContext.Done():
			if response != nil && response.Body != nil {
				_ = response.Body.Close()
			}
		}
	}()
	timer := time.NewTimer(idleTimeout)
	defer timer.Stop()
	select {
	case result := <-resultChannel:
		if result.err != nil {
			if streamState.operationContext.Err() != nil || requestContext.Err() != nil {
				return nil, abortedError(result.err)
			}
			return nil, llm.MustLlmError(
				fmt.Sprintf("DeepSeek API request to %s failed", baseURL),
				"TRANSPORT",
				llm.LlmErrorOptions{Cause: result.err},
			)
		}
		if result.response == nil {
			return nil, llm.MustLlmError("DeepSeek API returned no response", llm.EmptyResponseCode)
		}
		return result.response, nil
	case <-requestContext.Done():
		streamState.cancelOperation()
		return nil, abortedError(requestContext.Err())
	case <-streamState.operationContext.Done():
		return nil, abortedError(streamState.operationContext.Err())
	case <-timer.C:
		streamState.cancelOperation()
		return nil, llm.MustLlmError(
			fmt.Sprintf("DeepSeek stream idle timeout after %dms", idleTimeout.Milliseconds()),
			"TIMEOUT",
		)
	}
}

func (streamState *deepSeekStream) httpFailure(response *http.Response) error {
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
	retryAfter := providerRetryAfterMS(response.Header.Get("retry-after"), streamState.backend.now())
	requestID := response.Header.Get("x-request-id")
	if requestID == "" {
		requestID = response.Header.Get("x-deepseek-request-id")
	}
	status := response.StatusCode
	return llm.MustLlmError(message, HTTPErrorCode(status, providerError), llm.LlmErrorOptions{
		Status: &status, ProviderRetryAfterMS: retryAfter, RequestID: llm.ProviderRequestID(requestID),
	})
}

func (streamState *deepSeekStream) normalizeStreamError(requestContext context.Context, err error) error {
	var providerFailure *llm.LlmError
	if errors.As(err, &providerFailure) {
		return providerFailure
	}
	if requestContext.Err() != nil || streamState.operationContext.Err() != nil {
		streamState.cancelOperation()
		return abortedError(err)
	}
	return llm.MustLlmError(
		fmt.Sprintf("DeepSeek API stream from %s failed", streamState.baseURL),
		"TRANSPORT", llm.LlmErrorOptions{Cause: err},
	)
}

func (streamState *deepSeekStream) Close(closeContext context.Context) error {
	streamState.mu.Lock()
	if streamState.closed {
		streamState.mu.Unlock()
		return nil
	}
	streamState.closed = true
	downstream := streamState.downstream
	streamState.mu.Unlock()
	streamState.cancelOperation()
	if downstream != nil {
		return downstream.Close(closeContext)
	}
	return nil
}

func (streamState *deepSeekStream) markTerminated() {
	streamState.mu.Lock()
	streamState.terminated = true
	streamState.mu.Unlock()
}

func normalizeAPIKey(rawValue string, credentialRef string) (string, error) {
	trimmed := strings.TrimSpace(rawValue)
	if trimmed == "" {
		return "", llm.MustLlmError(
			fmt.Sprintf("llm-deepseek: the API key resolved from %s is blank; set %s to the raw key or export it in the launching environment", credentialRef, credentialRef),
			llm.InvalidCredentialCode,
		)
	}
	for _, character := range []byte(trimmed) {
		if character < 0x21 || character > 0x7e {
			return "", llm.MustLlmError(
				fmt.Sprintf("llm-deepseek: the API key resolved from %s contains characters no HTTP header can carry; set %s to the raw key alone", credentialRef, credentialRef),
				llm.InvalidCredentialCode,
			)
		}
	}
	return trimmed, nil
}

func abortedError(cause error) error {
	return llm.MustLlmError("DeepSeek request aborted by caller", "ABORTED", llm.LlmErrorOptions{Cause: cause})
}

func userAgent(version string) string {
	return fmt.Sprintf("%s/%s (+%s)", productName, version, productURL)
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
