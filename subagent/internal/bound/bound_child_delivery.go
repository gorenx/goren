package bound

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	boundcontract "github.com/gorenx/goren/subagent/bound"
)

func (child *boundChild) handleDelivery(
	requestContext context.Context,
	inputValue boundcontract.Input,
) (boundcontract.Receipt, error) {
	current, err := child.receivingEpoch(requestContext)
	if err != nil {
		return boundcontract.Receipt{}, err
	}
	childSession := current.handle.Subject.SessionValue()
	receiptValue, err := findInputReceipt(childSession, inputValue)
	if err != nil {
		return boundcontract.Receipt{}, err
	}
	if receiptValue.MessageID != "" {
		if err = child.sessions.Flush(requestContext, childSession); err != nil {
			return boundcontract.Receipt{}, err
		}
		return receiptValue, nil
	}
	deliverySource, err := boundcontract.NewDelivery(inputValue)
	if err != nil {
		return boundcontract.Receipt{}, err
	}
	messageValue, err := agentmessage.NewUserMessage(
		agentmessage.UserMessageInput{
			Content: inputValue.Content,
			Source:  deliverySource,
		},
	)
	if err != nil {
		return boundcontract.Receipt{}, err
	}
	if err = current.followup(requestContext, messageValue); err != nil {
		return boundcontract.Receipt{}, err
	}
	return boundcontract.Receipt{
		InputID:   inputValue.ID,
		MessageID: messageValue.StableID(),
	}, nil
}

func findInputReceipt(
	childSession session.Context,
	want boundcontract.Input,
) (boundcontract.Receipt, error) {
	if childSession == nil {
		return boundcontract.Receipt{}, errors.New(
			"subagent: Bound child Session is unavailable",
		)
	}
	startSeq := int64(0)
	if seedLength := childSession.Header().SeedLength; seedLength != nil {
		startSeq = *seedLength
	}
	var receiptValue boundcontract.Receipt
	found := false
	for _, committed := range childSession.Events() {
		if committed.Seq < startSeq ||
			committed.Type != agent.InboxSplicedEventName {
			continue
		}
		var splice agent.InboxSplice
		if err := deliveryJSONCodec.Unmarshal(
			committed.Data,
			&splice,
		); err != nil {
			return boundcontract.Receipt{}, fmt.Errorf(
				"subagent: decode child Inbox receipt at seq %d: %w",
				committed.Seq,
				err,
			)
		}
		for _, messageValue := range splice.Inserted {
			origin := messageValue.SourceValue()
			if origin == nil ||
				origin.SourceKind() != boundcontract.DeliveryKind {
				continue
			}
			deliveryValue, err := boundcontract.DecodeDelivery(origin)
			if err != nil {
				return boundcontract.Receipt{}, fmt.Errorf(
					"subagent: decode child delivery at seq %d: %w",
					committed.Seq,
					err,
				)
			}
			if deliveryValue.Input != want.ID {
				continue
			}
			if found {
				return boundcontract.Receipt{}, errors.New(
					"subagent: child Session contains duplicate Bound delivery receipts",
				)
			}
			if err = validateInputReceipt(
				messageValue,
				deliveryValue,
				want,
			); err != nil {
				return boundcontract.Receipt{}, err
			}
			receiptValue = boundcontract.Receipt{
				InputID:   want.ID,
				MessageID: messageValue.StableID(),
			}
			found = true
		}
	}
	return receiptValue, nil
}

func validateInputReceipt(
	messageValue agentmessage.UserMessage,
	received boundcontract.Delivery,
	want boundcontract.Input,
) error {
	expected, err := boundcontract.NewDelivery(want)
	if err != nil {
		return err
	}
	if !bytes.Equal(received.Origin, expected.Origin) {
		return errors.New(
			"subagent: Bound Input ID was reused with different provenance",
		)
	}
	receivedContent, err := deliveryJSONCodec.Marshal(
		messageValue.ContentValue(),
	)
	if err != nil {
		return err
	}
	expectedContent, err := deliveryJSONCodec.Marshal(want.Content)
	if err != nil {
		return err
	}
	if !bytes.Equal(receivedContent, expectedContent) {
		return errors.New(
			"subagent: Bound Input ID was reused with different content",
		)
	}
	return nil
}

var deliveryJSONCodec = sonic.Config{
	SortMapKeys:           true,
	UseUnicodeErrors:      true,
	DisallowUnknownFields: true,
	CopyString:            true,
	ValidateString:        true,
	CaseSensitive:         true,
}.Froze()
