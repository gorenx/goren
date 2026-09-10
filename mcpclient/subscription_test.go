package mcpclient

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestSubscriptionRequiresCorrelationAndHonorsAcceptedFilter(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		feed := newSubscription()
		acknowledgement := fixtureAck
		if !enabled {
			acknowledgement = strings.ReplaceAll(acknowledgement, `"toolsListChanged":true`, `"toolsListChanged":false`)
		}
		var notice mcp.JSONRPCNotification
		if err := json.Unmarshal([]byte(acknowledgement), &notice); err != nil {
			t.Fatal(err)
		}
		feed.receive(notice)
		if err := <-feed.ack; err != nil {
			t.Fatal("a valid empty acknowledgement must still establish liveness", err)
		}
		if err := json.Unmarshal([]byte(strings.ReplaceAll(fixtureChanged, "fixture-subscription", "other-stream")), &notice); err != nil {
			t.Fatal(err)
		}
		feed.receive(notice)
		select {
		case <-feed.changes:
			t.Fatal("foreign subscription changed catalog")
		default:
		}
		if err := json.Unmarshal([]byte(fixtureChanged), &notice); err != nil {
			t.Fatal(err)
		}
		feed.receive(notice)
		select {
		case <-feed.changes:
			if !enabled {
				t.Fatal("notification outside accepted filter")
			}
		default:
			if enabled {
				t.Fatal("missing subscribed change")
			}
		}
	}
}
