package apiproxy_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorenx/goren/apiproxy"
	"github.com/gorenx/goren/connection"
)

func TestPendingResponseAcceptsExactlyOnce(t *testing.T) {
	t.Parallel()
	methods := apiproxy.NewCatalog()
	waiting, err := apiproxy.RegisterPendingResponse(methods, "pending-1", decodeTextResponse)
	if err != nil {
		t.Fatal(err)
	}

	message := textResponse(t, "pending-1", "allowed")
	receipt, err := methods.Respond(context.Background(), message)
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.Accepted {
		t.Fatalf("receipt = %#v", receipt)
	}

	value, err := waiting.Wait(context.Background())
	if err != nil || value != "allowed" {
		t.Fatalf("value = %q, err = %v", value, err)
	}
	duplicate, err := methods.Respond(context.Background(), message)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.Accepted || duplicate.Reason != connection.ReceiptNotPending {
		t.Fatalf("duplicate receipt = %#v", duplicate)
	}
}

func TestBadPendingResponseRemainsRetryable(t *testing.T) {
	t.Parallel()
	methods := apiproxy.NewCatalog()
	waiting, err := apiproxy.RegisterPendingResponse(methods, "pending-retry", decodeTextResponse)
	if err != nil {
		t.Fatal(err)
	}
	badResult, err := connection.Success(struct {
		Unexpected bool `json:"unexpected"`
	}{Unexpected: true})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := methods.Respond(context.Background(), connection.ClientResponse{
		Type: connection.ClientResponseType, RPCID: "pending-retry", Result: badResult,
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Accepted || receipt.Reason != connection.ReceiptBadResponse {
		t.Fatalf("bad receipt = %#v", receipt)
	}

	receipt, err = methods.Respond(context.Background(), textResponse(t, "pending-retry", "corrected"))
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.Accepted {
		t.Fatalf("retry receipt = %#v", receipt)
	}
	value, err := waiting.Wait(context.Background())
	if err != nil || value != "corrected" {
		t.Fatalf("value = %q, err = %v", value, err)
	}
}

func TestPendingResponseCancellationWithdrawsEntry(t *testing.T) {
	t.Parallel()
	methods := apiproxy.NewCatalog()
	waiting, err := apiproxy.RegisterPendingResponse(methods, "pending-cancel", decodeTextResponse)
	if err != nil {
		t.Fatal(err)
	}
	waitContext, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := waiting.Wait(waitContext); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error = %v", err)
	}
	receipt, err := methods.Respond(context.Background(), textResponse(t, "pending-cancel", "late"))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Accepted || receipt.Reason != connection.ReceiptNotPending {
		t.Fatalf("late receipt = %#v", receipt)
	}
}

func TestPendingResponseRejectsDuplicateRegistration(t *testing.T) {
	t.Parallel()
	methods := apiproxy.NewCatalog()
	waiting, err := apiproxy.RegisterPendingResponse(methods, "same-id", decodeTextResponse)
	if err != nil {
		t.Fatal(err)
	}
	defer waiting.Withdraw(nil)
	if _, err := apiproxy.RegisterPendingResponse(methods, "same-id", decodeTextResponse); err == nil {
		t.Fatal("duplicate pending rpcId was accepted")
	}
}

func TestConcurrentPendingResponsesHaveOneWinner(t *testing.T) {
	t.Parallel()
	methods := apiproxy.NewCatalog()
	waiting, err := apiproxy.RegisterPendingResponse(methods, "pending-race", decodeTextResponse)
	if err != nil {
		t.Fatal(err)
	}

	type attempt struct {
		value   string
		receipt connection.RPCReceipt
		err     error
	}
	start := make(chan struct{})
	attempts := make(chan attempt, 2)
	var workers sync.WaitGroup
	for _, candidate := range []string{"first", "second"} {
		workers.Add(1)
		go func(value string) {
			defer workers.Done()
			<-start
			receipt, respondErr := methods.Respond(context.Background(), textResponse(t, "pending-race", value))
			attempts <- attempt{value: value, receipt: receipt, err: respondErr}
		}(candidate)
	}
	close(start)
	workers.Wait()
	close(attempts)

	acceptedValue := ""
	acceptedCount := 0
	for outcome := range attempts {
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		if outcome.receipt.Accepted {
			acceptedCount++
			acceptedValue = outcome.value
		} else if outcome.receipt.Reason != connection.ReceiptNotPending {
			t.Fatalf("losing receipt = %#v", outcome.receipt)
		}
	}
	if acceptedCount != 1 {
		t.Fatalf("accepted responses = %d", acceptedCount)
	}
	waitContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	value, err := waiting.Wait(waitContext)
	if err != nil || value != acceptedValue {
		t.Fatalf("value = %q, accepted = %q, err = %v", value, acceptedValue, err)
	}
}

func TestPendingDecoderFailureDoesNotConsumeEntry(t *testing.T) {
	t.Parallel()
	methods := apiproxy.NewCatalog()
	var failed atomic.Bool
	decodeResponse := func(result connection.RPCResult) (string, bool) {
		if failed.CompareAndSwap(false, true) {
			panic("decoder crashed")
		}
		return decodeTextResponse(result)
	}
	waiting, err := apiproxy.RegisterPendingResponse(methods, "pending-failure", decodeResponse)
	if err != nil {
		t.Fatal(err)
	}
	message := textResponse(t, "pending-failure", "recovered")
	if _, err := methods.Respond(context.Background(), message); err == nil {
		t.Fatal("decoder panic was not returned as a technical failure")
	}
	receipt, err := methods.Respond(context.Background(), message)
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.Accepted {
		t.Fatalf("retry receipt = %#v", receipt)
	}
	value, err := waiting.Wait(context.Background())
	if err != nil || value != "recovered" {
		t.Fatalf("value = %q, err = %v", value, err)
	}
}

func decodeTextResponse(result connection.RPCResult) (string, bool) {
	if !result.OK || result.Error != nil || len(result.Value) == 0 {
		return "", false
	}
	var value string
	if err := json.Unmarshal(result.Value, &value); err != nil || string(result.Value) == "null" {
		return "", false
	}
	return value, true
}

func textResponse(t *testing.T, correlationID connection.RPCID, value string) connection.ClientResponse {
	t.Helper()
	result, err := connection.Success(value)
	if err != nil {
		t.Fatal(err)
	}
	return connection.ClientResponse{Type: connection.ClientResponseType, RPCID: correlationID, Result: result}
}
