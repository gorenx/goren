package deepseek

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gorenx/goren/llm"
)

const (
	harnessProductName = "deepseek-harness"
	harnessProjectURL  = "https://github.com/deepseek-ai/deepseek-harness"
)

type responseResult struct {
	response *http.Response
	err      error
}

func (backend *Adapter) openStream(
	requestContext context.Context,
	operationContext context.Context,
	cancelOperation context.CancelFunc,
	requestOptions llm.GenerateOptions,
) (llm.ChunkStream, string, error) {
	connection, err := backend.connections.CurrentConnection()
	if err != nil {
		return nil, "", err
	}
	apiKey, err := backend.credentials.ResolveAPIKey(requestContext, connection)
	if err != nil {
		return nil, "", err
	}
	apiKey, err = normalizeAPIKey(apiKey, connection.APIKeyEnv)
	if err != nil {
		return nil, "", err
	}
	identityValue, err := backend.identity.UserID()
	if err != nil {
		return nil, "", err
	}
	if identityValue == "" {
		return nil, "", errors.New("llm-deepseek: anonymous user id is empty")
	}
	wireValue, err := SerializeRequest(requestOptions, connection.Defaults)
	if err != nil {
		return nil, "", err
	}
	payload, err := json.Marshal(wireValue)
	if err != nil {
		return nil, "", fmt.Errorf("llm-deepseek: encode request: %w", err)
	}
	endpoint := connection.BaseURL + "/chat/completions"
	httpRequest, err := http.NewRequestWithContext(
		operationContext, http.MethodPost, endpoint, bytes.NewReader(payload),
	)
	if err != nil {
		return nil, "", err
	}
	httpRequest.Header.Set("authorization", "Bearer "+apiKey)
	httpRequest.Header.Set("content-type", "application/json")
	httpRequest.Header.Set("accept", "text/event-stream")
	httpRequest.Header.Set("user-agent", userAgent(backend.version))
	httpRequest.Header.Set("x-deepseek-harness-user-id", identityValue)
	if requestOptions.SessionID != "" {
		httpRequest.Header.Set("x-deepseek-harness-session-id", requestOptions.SessionID)
	}
	if requestOptions.Purpose == llm.PurposeCompaction {
		httpRequest.Header.Set("x-deepseek-harness-compact", "1")
	}

	response, err := backend.doRequest(
		requestContext,
		operationContext,
		cancelOperation,
		httpRequest,
		connection.BaseURL,
		connection.StreamIdleTimeout,
	)
	if err != nil {
		return nil, "", err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, "", backend.httpFailure(response)
	}
	if response.Body == nil || response.Body == http.NoBody {
		return nil, "", llm.MustLlmError("DeepSeek API returned no response body", llm.EmptyResponseCode)
	}
	payloads, err := newSSEPayloadStream(response.Body, connection.StreamIdleTimeout, nil)
	if err != nil {
		_ = response.Body.Close()
		return nil, "", err
	}
	downstream, err := translatePayloads(payloads)
	if err != nil {
		_ = payloads.Close(context.Background())
		return nil, "", err
	}
	return downstream, connection.BaseURL, nil
}

func (backend *Adapter) doRequest(
	requestContext context.Context,
	operationContext context.Context,
	cancelOperation context.CancelFunc,
	httpRequest *http.Request,
	baseURL string,
	idleTimeout time.Duration,
) (*http.Response, error) {
	resultChannel := make(chan responseResult)
	go func() {
		response, err := backend.requests.Do(httpRequest)
		result := responseResult{
			response: response,
			err:      err,
		}
		select {
		case resultChannel <- result:
		case <-operationContext.Done():
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
			if operationContext.Err() != nil || requestContext.Err() != nil {
				return nil, abortedError(result.err)
			}
			return nil, llm.MustLlmError(
				fmt.Sprintf("DeepSeek API request to %s failed", baseURL),
				"TRANSPORT",
				llm.LlmErrorOptions{
					Cause: result.err,
				},
			)
		}
		if result.response == nil {
			return nil, llm.MustLlmError("DeepSeek API returned no response", llm.EmptyResponseCode)
		}
		return result.response, nil
	case <-requestContext.Done():
		cancelOperation()
		return nil, abortedError(requestContext.Err())
	case <-operationContext.Done():
		return nil, abortedError(operationContext.Err())
	case <-timer.C:
		cancelOperation()
		return nil, llm.MustLlmError(
			fmt.Sprintf("DeepSeek stream idle timeout after %dms", idleTimeout.Milliseconds()),
			"TIMEOUT",
		)
	}
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
	return llm.MustLlmError(
		"DeepSeek request aborted by caller",
		"ABORTED",
		llm.LlmErrorOptions{
			Cause: cause,
		},
	)
}

func userAgent(version string) string {
	return fmt.Sprintf("%s/%s (+%s)", harnessProductName, version, harnessProjectURL)
}
