package settingsapi

import (
	"context"
	"encoding/json"
	rpc "github.com/gorenx/goren/apiproxy/rpc"
)

// DescribeMethod is the loopback-only Settings catalog endpoint.
const DescribeMethod = "settings.describe"

// DescribeRequest is the empty settings.describe payload.
type DescribeRequest struct{}

// ApplyMode identifies when an accepted setting takes effect.
type ApplyMode string

const (
	ApplyLive    ApplyMode = "live"
	ApplyRestart ApplyMode = "restart"
)

// SecretView reports a redacted secret slot without its value.
type SecretView struct {
	Path []string `json:"path"`
	Set  bool     `json:"set"`
}

// NamespaceView is one redacted Settings namespace snapshot.
type NamespaceView struct {
	NS       string           `json:"ns"`
	Schema   json.RawMessage  `json:"schema"`
	Value    json.RawMessage  `json:"value"`
	Base     *json.RawMessage `json:"base,omitempty"`
	User     *json.RawMessage `json:"user,omitempty"`
	Applies  ApplyMode        `json:"applies"`
	Secrets  []SecretView     `json:"secrets"`
	Revision int64            `json:"revision"`
}

// DescribeValue is the complete browser-visible Settings catalog.
type DescribeValue struct {
	Writable    bool            `json:"writable"`
	HasDocument bool            `json:"hasDocument"`
	Namespaces  []NamespaceView `json:"namespaces"`
}

// API owns the currently included Settings read method.
type API interface {
	Describe(context.Context, rpc.Request[DescribeRequest]) (rpc.Outcome[DescribeValue], error)
}

// Register installs settings.describe without claiming the
// separately scoped document and mutation methods.
func Register(methods *rpc.Catalog, endpoint API) error {
	return rpc.RegisterUnary(methods, DescribeMethod, rpc.DecodeObject[DescribeRequest], endpoint.Describe)
}
