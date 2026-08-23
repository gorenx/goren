package continuation

import (
	"testing"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

func TestLastAssistantDoesNotReuseEarlierActivationOutput(t *testing.T) {
	conversation, createErr := session.New(
		"activation-output",
		session.CreateOptions{},
	)
	if createErr != nil {
		t.Fatal(createErr)
	}
	appendAssistantMessage(t, conversation, 1, "earlier answer")
	boundary := conversation.Seq()

	output, outputErr := lastAssistant(conversation, boundary)
	if outputErr != nil {
		t.Fatal(outputErr)
	}
	if len(output) != 0 {
		t.Fatalf("current Activation output = %#v", output)
	}

	appendAssistantMessage(t, conversation, 2, "current answer")
	output, outputErr = lastAssistant(conversation, boundary)
	if outputErr != nil {
		t.Fatal(outputErr)
	}
	if len(output) != 1 {
		t.Fatalf("current Activation output = %#v", output)
	}
	textBlock, matches := output[0].(llm.TextBlock)
	if !matches || textBlock.Text != "current answer" {
		t.Fatalf("current Activation output = %#v", output)
	}
}

func appendAssistantMessage(
	t *testing.T,
	conversation *session.Session,
	turnNumber int64,
	text string,
) {
	t.Helper()
	messageValue, messageErr := llm.NewAssistantMessage(
		llm.AssistantMessageInput{
			Content: []llm.ContentBlock{
				llm.NewTextBlock(text),
			},
			Source: llm.ModelMessageSource{
				Provider: "mock",
				Model:    "model",
			},
		},
	)
	if messageErr != nil {
		t.Fatal(messageErr)
	}
	if _, appendErr := session.AppendSurface(
		conversation,
		session.AssistantMessaged,
		session.AssistantMessage{
			Turn:    turnNumber,
			Step:    1,
			Message: messageValue,
		},
		session.SurfaceIntent{
			Operation: session.SurfaceAppend(),
		},
	); appendErr != nil {
		t.Fatal(appendErr)
	}
}
