package mcpclient

import (
	"encoding/json"
	"errors"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"
)

// subscription contains notification state only. SDK callbacks borrow it without
// retaining the Connection that owns the SDK and its lifecycle.
type subscription struct {
	mutex        sync.Mutex
	ack          chan error
	ackOnce      sync.Once
	identifier   string
	toolsChanged bool
	changes      chan struct{}
}

func newSubscription() *subscription {
	return &subscription{
		ack:     make(chan error, 1),
		changes: make(chan struct{}, 1),
	}
}

func (feed *subscription) receive(input mcp.JSONRPCNotification) {
	if input.Method == mcp.MethodNotificationSubscriptionsAcknowledged {
		feed.ackOnce.Do(func() {
			encoded, _ := json.Marshal(input.Params)
			var acknowledgement mcp.SubscriptionsAcknowledgedParams
			err := json.Unmarshal(encoded, &acknowledgement)
			identifier, _ := acknowledgement.Meta[mcp.MetaKeySubscriptionID].(string)
			if err != nil || identifier == "" {
				feed.ack <- errors.New("mcpclient: subscription was not acknowledged")
				return
			}
			feed.mutex.Lock()
			feed.identifier = identifier
			feed.toolsChanged = acknowledgement.Notifications.ToolsListChanged
			feed.mutex.Unlock()
			feed.ack <- nil
		})
		return
	}
	if input.Method != mcp.MethodNotificationToolsListChanged {
		return
	}
	encoded, _ := json.Marshal(input.Params)
	var params struct {
		Meta map[string]any `json:"_meta"`
	}
	if json.Unmarshal(encoded, &params) != nil {
		return
	}
	feed.mutex.Lock()
	matched := feed.toolsChanged &&
		feed.identifier != "" &&
		params.Meta[mcp.MetaKeySubscriptionID] == feed.identifier
	feed.mutex.Unlock()
	if matched {
		select {
		case feed.changes <- struct{}{}:
		default:
		}
	}
}
