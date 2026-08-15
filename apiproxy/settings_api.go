package apiproxy

import (
	"context"
	"encoding/json"
)

// SettingsDescribeMethod is the loopback-only Settings catalog endpoint.
const SettingsDescribeMethod = "settings.describe"

// SettingsDescribeRequest is the empty settings.describe payload.
type SettingsDescribeRequest struct{}

// SettingsApplyMode identifies when an accepted setting takes effect.
type SettingsApplyMode string

const (
	SettingsApplyLive    SettingsApplyMode = "live"
	SettingsApplyRestart SettingsApplyMode = "restart"
)

// SettingsSecretView reports a redacted secret slot without its value.
type SettingsSecretView struct {
	Path []string `json:"path"`
	Set  bool     `json:"set"`
}

// SettingsNamespaceView is one redacted Settings namespace snapshot.
type SettingsNamespaceView struct {
	NS       string               `json:"ns"`
	Schema   json.RawMessage      `json:"schema"`
	Value    json.RawMessage      `json:"value"`
	Base     *json.RawMessage     `json:"base,omitempty"`
	User     *json.RawMessage     `json:"user,omitempty"`
	Applies  SettingsApplyMode    `json:"applies"`
	Secrets  []SettingsSecretView `json:"secrets"`
	Revision int64                `json:"revision"`
}

// SettingsDescribeValue is the complete browser-visible Settings catalog.
type SettingsDescribeValue struct {
	Writable    bool                    `json:"writable"`
	HasDocument bool                    `json:"hasDocument"`
	Namespaces  []SettingsNamespaceView `json:"namespaces"`
}

// SettingsDescribeAPI owns the currently included Settings read method.
type SettingsDescribeAPI interface {
	Describe(context.Context, Request[SettingsDescribeRequest]) (Outcome[SettingsDescribeValue], error)
}

// RegisterSettingsDescribeAPI installs settings.describe without claiming the
// separately scoped document and mutation methods.
func RegisterSettingsDescribeAPI(methods *Catalog, gateway SettingsDescribeAPI) error {
	return RegisterUnary(methods, SettingsDescribeMethod, DecodeObject[SettingsDescribeRequest], gateway.Describe)
}
