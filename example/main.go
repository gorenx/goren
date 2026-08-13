package main

import (
	"context"
	"fmt"
	"os"

	"github.com/gorenx/goren/llm"
	openaiadapter "github.com/gorenx/goren/llm/adapter/openai"
)

func main() {
	credential := os.Getenv("OPENAI_API_KEY")
	if credential == "" {
		fmt.Println("set OPENAI_API_KEY to run this example")
		return
	}

	targetModel := llm.Model{
		ID:              "gpt-4.1-mini",
		Name:            "GPT-4.1 mini",
		API:             llm.APIOpenAICompletions,
		Provider:        "openai",
		BaseURL:         "https://api.openai.com/v1",
		Input:           []llm.InputModality{llm.InputText},
		ContextWindow:   1_047_576,
		MaxOutputTokens: 4_096,
	}
	adapterRegistry := llm.NewRegistry()
	if err := adapterRegistry.Register(
		llm.APIOpenAICompletions,
		func(targetModel llm.Model) (llm.APIAdapter, error) {
			return openaiadapter.New(targetModel, nil)
		},
	); err != nil {
		panic(err)
	}
	llmClient, err := llm.NewClient(targetModel, adapterRegistry, nil)
	if err != nil {
		panic(err)
	}
	assistantReply, err := llmClient.Complete(
		context.Background(),
		llm.Context{
			Messages: []llm.Message{llm.NewTextMessage("Reply with exactly: hello")},
		},
		llm.StreamOptions{APIKey: credential},
	)
	if err != nil {
		panic(err)
	}
	if assistantReply.StopReason == llm.StopReasonError || assistantReply.StopReason == llm.StopReasonAborted {
		panic(assistantReply.ErrorMessage)
	}
	fmt.Println(llm.Text(assistantReply))
}
