package runtimeactor

import (
	"context"
	"errors"

	goakt "github.com/tochemey/goakt/v4/actor"
)

// connectionRetry owns one transport recovery request, independently of Turn work.
type connectionRetry struct {
	name   string
	reply  *goakt.GrainReply
	cancel context.CancelFunc
}

type connectionRetried struct {
	request *connectionRetry
	err     error
}

func (owner *Runtime) receiveConnections(gc *goakt.GrainContext) bool {
	switch input := gc.Message().(type) {
	case ReadConnections:
		if owner.creationID == "" || owner.creationID != input.CreationID || owner.connections == nil {
			gc.Response(Connections{
				Err: errors.New("tools: connections do not belong to this runtime creation"),
			})
			return true
		}
		entries := owner.connections.list()
		for index := range entries {
			if owner.retries[entries[index].Name] != nil {
				entries[index].Status = ConnectionReconnecting
			}
		}
		gc.Response(Connections{
			Values: entries,
		})
	case RetryConnection:
		if owner.creationID == "" ||
			owner.creationID != input.CreationID ||
			owner.connections == nil ||
			input.Context == nil {
			gc.Response(Completion{
				Err: errors.New("tools: connection retry requires the current creation and context"),
			})
			return true
		}
		if owner.request != nil || owner.retries[input.Name] != nil {
			gc.Response(Completion{
				Err: errors.New("tools: connection lifecycle operation is already running"),
			})
			return true
		}
		peer := owner.connections.find(input.Name)
		if peer == nil {
			gc.Response(Completion{
				Err: errors.New("tools: selected MCP connection was not found"),
			})
			return true
		}
		reply := gc.DeferResponse()
		if reply == nil {
			gc.Response(Completion{
				Err: errors.New("tools: connection retry requires an asynchronous request"),
			})
			return true
		}
		work, cancel := context.WithCancel(input.Context)
		request := &connectionRetry{
			name:   input.Name,
			reply:  reply,
			cancel: cancel,
		}
		if owner.retries == nil {
			owner.retries = make(map[string]*connectionRetry)
		}
		owner.retries[input.Name] = request
		if err := gc.PipeToSelf(func() (any, error) {
			return connectionRetried{
				request: request,
				err:     peer.Reconnect(work),
			}, nil
		}); err != nil {
			delete(owner.retries, input.Name)
			cancel()
			reply.Response(Completion{
				Err: err,
			})
		}
	case connectionRetried:
		gc.NoErr()
		request := input.request
		if owner.retries[request.name] != request {
			return true
		}
		delete(owner.retries, request.name)
		request.cancel()
		// Recovery may discover different tools, including when retirement failed
		// after transport selection. Applying those definitions remains ordinary work.
		owner.changed = true
		request.reply.Response(Completion{
			Err: input.err,
		})
	default:
		return false
	}
	return true
}
