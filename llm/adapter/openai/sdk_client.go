package openai

import (
	"fmt"
	"net/http"

	"github.com/gorenx/goren/llm"
	officialopenai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

func newSDKClient(targetModel llm.Model, httpClient *http.Client) officialopenai.Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return officialopenai.NewClient(
		option.WithBaseURL(targetModel.BaseURL),
		option.WithHTTPClient(httpClient),
	)
}

func validateAdapterModel(targetModel llm.Model, expectedAPI llm.API) error {
	if err := llm.ValidateModel(targetModel); err != nil {
		return err
	}
	if targetModel.API != expectedAPI {
		return fmt.Errorf("%w: got %q, want %q", llm.ErrAPIMismatch, targetModel.API, expectedAPI)
	}
	return nil
}
