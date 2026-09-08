package meteractor_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gorenx/goren/llm"
	meteractor "github.com/gorenx/goren/llm/tokenmeter/actor"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type request struct {
	target  *goakt.GrainIdentity
	message any
}

type reply struct {
	value any
	err   error
}

type requester struct{ replies chan reply }

func (*requester) PreStart(*goakt.Context) error { return nil }
func (*requester) PostStop(*goakt.Context) error { return nil }
func (driver *requester) Receive(rc *goakt.ReceiveContext) {
	if input, ok := rc.Message().(request); ok {
		replies := driver.replies
		rc.RequestGrain(input.target, input.message).Then(func(value any, failure error) {
			replies <- reply{
				value: value,
				err:   failure,
			}
		})
	}
}

func TestGrainIsolatesCreationsAndRejectsLateMeasurements(t *testing.T) {
	ctx := context.Background()
	system, err := goakt.NewActorSystem("meter-test", goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		deadline, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if err := system.Stop(deadline); err != nil {
			t.Error(err)
		}
	})
	identity, err := goakt.GrainOf[*meteractor.Grain](ctx, system, meteractor.RuntimeName("chat"), goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	replies := make(chan reply, 1)
	driver, err := system.Spawn(ctx, "requester", &requester{
		replies: replies,
	}, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	invoke := func(message any) any {
		t.Helper()
		if err := goakt.Tell(ctx, driver, request{
			target:  identity,
			message: message,
		}); err != nil {
			t.Fatal(err)
		}
		select {
		case result := <-replies:
			if result.err != nil {
				t.Fatal(result.err)
			}
			return result.value
		case <-time.After(3 * time.Second):
			t.Fatal("request timed out")
			return nil
		}
	}
	prompt := strings.Repeat("large prompt ", 100)
	payload, err := json.Marshal(session.RequestHeaderSnapshot{
		Header: session.EpochHeader{
			Config: llm.CallConfig{
				Provider: "mock",
				Model:    "model",
			},
			System: &prompt,
		},
		Reason: session.RequestHeaderInitial,
	})
	if err != nil {
		t.Fatal(err)
	}
	first := meteractor.Measure{
		SessionID:  "chat",
		CreationID: "A",
		Events: []session.Event{
			{
				Seq:  0,
				Type: session.RequestHeaderEventName,
				Data: payload,
			},
		},
	}
	measured := invoke(first).(meteractor.Measured)
	if measured.Err != nil || measured.Value.TotalTokens == 0 {
		t.Fatalf("first = %#v", measured)
	}
	second := meteractor.Measure{
		SessionID:  "chat",
		CreationID: "B",
		Events: []session.Event{
			{
				Seq:  0,
				Type: "fixture",
			},
		},
	}
	if result := invoke(second).(meteractor.Measured); result.Err != nil || result.Value.TotalTokens != 0 {
		t.Fatalf("creation reused prior fold: %#v", result)
	}
	for range 2 {
		if result := invoke(meteractor.Close{
			SessionID:  "chat",
			CreationID: "A",
		}).(meteractor.Completion); result.Err != nil {
			t.Fatal(result.Err)
		}
	}
	if result := invoke(first).(meteractor.Measured); result.Err == nil {
		t.Fatal("closed creation measured again")
	}
	if result := invoke(second).(meteractor.Measured); result.Err != nil {
		t.Fatal(result.Err)
	}
	second.SessionID = "another"
	if result := invoke(second).(meteractor.Measured); result.Err == nil {
		t.Fatal("wrong Session accepted")
	}
	second.SessionID = "chat"
	if result := invoke(sessionactor.ClosedNotice{
		SessionID:  "chat",
		CreationID: "B",
	}).(meteractor.Completion); result.Err != nil {
		t.Fatal(result.Err)
	}
	if result := invoke(second).(meteractor.Measured); result.Err == nil {
		t.Fatal("closed notice was ignored")
	}
}
