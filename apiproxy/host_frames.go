package apiproxy

import (
	"encoding/json"
	"errors"
	"fmt"
)

// HostRemoteEventFrame forwards one allowlisted Host event. Args retain the
// event owner's JSON value contract.
type HostRemoteEventFrame struct {
	Event string            `json:"event"`
	Args  []json.RawMessage `json:"args"`
}

func (HostRemoteEventFrame) HostFrame()   {}
func (HostRemoteEventFrame) Type() string { return "host/remote-event" }
func (item HostRemoteEventFrame) Validate() error {
	if item.Event == "" {
		return errors.New("remote event name must be non-empty")
	}
	if item.Args == nil {
		return errors.New("remote event args must be an array")
	}
	for index, argument := range item.Args {
		if argument != nil && !json.Valid(argument) {
			return fmt.Errorf("remote event arg %d must be JSON", index)
		}
	}
	return nil
}
