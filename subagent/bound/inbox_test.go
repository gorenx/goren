package bound_test

import (
	"encoding/json"
	"testing"

	"github.com/gorenx/goren/agentmessage"
	boundcontract "github.com/gorenx/goren/subagent/bound"
)

func TestDeliverySurvivesOpaqueMessageSourceReplay(testingContext *testing.T) {
	testingContext.Parallel()
	inputValue := boundcontract.Input{
		ID: "source:42",
		Content: []agentmessage.ContentBlock{
			agentmessage.NewTextBlock("relay payload"),
		},
		Source: agentmessage.PluginMessageSource{
			Plugin: "test-source",
			Form:   agentmessage.ContextRelay,
		},
	}
	deliveryValue, err := boundcontract.NewDelivery(inputValue)
	if err != nil {
		testingContext.Fatal(err)
	}
	messageValue, err := agentmessage.NewUserMessage(
		agentmessage.UserMessageInput{
			Content: inputValue.Content,
			Source:  deliveryValue,
		},
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	rawValue, err := json.Marshal(messageValue)
	if err != nil {
		testingContext.Fatal(err)
	}
	restored, err := agentmessage.DecodeUserMessage(rawValue)
	if err != nil {
		testingContext.Fatal(err)
	}
	restoredDelivery, err := boundcontract.DecodeDelivery(
		restored.SourceValue(),
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	if restoredDelivery.Input != inputValue.ID ||
		restoredDelivery.Form != agentmessage.ContextRelay ||
		string(restoredDelivery.Origin) != string(deliveryValue.Origin) {
		testingContext.Fatalf(
			"restored Delivery = %#v, want %#v",
			restoredDelivery,
			deliveryValue,
		)
	}
}

func TestSnapshotInputRejectsIncompleteIdentityAndDetachesContent(
	testingContext *testing.T,
) {
	testingContext.Parallel()
	if _, err := boundcontract.SnapshotInput(boundcontract.Input{}); err == nil {
		testingContext.Fatal("empty Input succeeded")
	}
	content := []agentmessage.ContentBlock{
		agentmessage.NewTextBlock("original"),
	}
	detached, err := boundcontract.SnapshotInput(boundcontract.Input{
		ID:      "stable-input",
		Content: content,
		Source:  agentmessage.UserMessageSource{},
	})
	if err != nil {
		testingContext.Fatal(err)
	}
	content[0] = agentmessage.NewTextBlock("changed")
	plain, matches := detached.Content[0].(agentmessage.PlainTextContent)
	if !matches {
		testingContext.Fatalf("detached Content = %#v", detached.Content)
	}
	text, found := plain.PlainText()
	if !found || text != "original" {
		testingContext.Fatalf("detached text = %q, %t", text, found)
	}
}
