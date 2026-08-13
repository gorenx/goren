package main

import (
	"context"
	"fmt"
	"github.com/gorenx/goren/llm"
	openaiadapter "github.com/gorenx/goren/llm/adapter/openai"
	"os"
)

func main() {
	credential := os.Getenv("OPENAI_API_KEY")
	if credential == "" {
		fmt.Println("set OPENAI_API_KEY to run this example")
		return
	}

	adapterRegistry := llm.NewRegistry()
	if err := adapterRegistry.Register(openaiadapter.New(nil), "built-in"); err != nil {
		panic(err)
	}
	llmClient, err := llm.NewClient(adapterRegistry, nil)
	if err != nil {
		panic(err)
	}
	assistantReply, err := llmClient.Complete(
		context.Background(),
		llm.Model{
			ID:              "gpt-4.1-mini",
			Name:            "GPT-4.1 mini",
			API:             llm.APIOpenAICompletions,
			Provider:        "openai",
			BaseURL:         "https://api.openai.com/v1",
			Input:           []llm.InputModality{llm.InputText},
			ContextWindow:   1_047_576,
			MaxOutputTokens: 4_096,
		},
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
