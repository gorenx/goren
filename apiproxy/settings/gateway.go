package settingsapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	rpc "github.com/gorenx/goren/apiproxy/rpc"

	"github.com/gorenx/goren/connection"
)

const settingsAbsentMessage = "settings service is absent: this deployment does not mount a settings provider (e.g. @deepseek-ai/dsh-settings-file) in its composition"

// Describer is the consumer-owned redacted Settings catalog capability.
type Describer interface {
	DescribeSettings(context.Context) (DescribeValue, error)
}

// Gateway projects an optional Settings Provider onto the Host API.
type Gateway struct {
	describer Describer
}

// NewGateway creates a Settings projection. A nil Provider preserves
// the source API's explicit absent-service business failure.
func NewGateway(source Describer) *Gateway {
	return &Gateway{
		describer: source,
	}
}

// Describe returns a redacted catalog or the canonical absent-service failure.
func (owner *Gateway) Describe(
	requestContext context.Context,
	_ rpc.Request[DescribeRequest],
) (rpc.Outcome[DescribeValue], error) {
	if owner.describer == nil {
		return rpc.Fail[DescribeValue](rpc.NewRPCError(
			connection.ErrorInternal, settingsAbsentMessage, struct{}{},
		)), nil
	}
	description, err := owner.describer.DescribeSettings(requestContext)
	if err != nil {
		return rpc.Outcome[DescribeValue]{}, err
	}
	validated, err := validateSettingsDescription(description)
	if err != nil {
		return rpc.Outcome[DescribeValue]{}, err
	}
	return rpc.OK(validated), nil
}

func validateSettingsDescription(description DescribeValue) (DescribeValue, error) {
	namespaces := make([]NamespaceView, 0, len(description.Namespaces))
	seen := make(map[string]struct{}, len(description.Namespaces))
	for _, namespace := range description.Namespaces {
		if namespace.NS == "" {
			return DescribeValue{}, errors.New("apiproxy: Settings Provider returned an empty namespace")
		}
		if _, exists := seen[namespace.NS]; exists {
			return DescribeValue{}, fmt.Errorf("apiproxy: Settings Provider returned duplicate namespace %q", namespace.NS)
		}
		seen[namespace.NS] = struct{}{}
		if namespace.Applies != ApplyLive && namespace.Applies != ApplyRestart {
			return DescribeValue{}, fmt.Errorf(
				"apiproxy: Settings namespace %q returned invalid applies %q", namespace.NS, namespace.Applies,
			)
		}
		if namespace.Revision < 0 {
			return DescribeValue{}, fmt.Errorf(
				"apiproxy: Settings namespace %q returned negative revision", namespace.NS,
			)
		}
		if !json.Valid(namespace.Schema) ||
			!json.Valid(namespace.Value) ||
			!validOptionalJSON(namespace.Base) ||
			!validOptionalJSON(namespace.User) {
			return DescribeValue{}, fmt.Errorf(
				"apiproxy: Settings namespace %q returned invalid JSON", namespace.NS,
			)
		}
		namespace.Schema = append(json.RawMessage(nil), namespace.Schema...)
		namespace.Value = append(json.RawMessage(nil), namespace.Value...)
		namespace.Base = cloneRawMessagePointer(namespace.Base)
		namespace.User = cloneRawMessagePointer(namespace.User)
		secrets := make([]SecretView, 0, len(namespace.Secrets))
		for _, secret := range namespace.Secrets {
			secrets = append(secrets, SecretView{
				Path: append([]string{}, secret.Path...),
				Set:  secret.Set,
			})
		}
		namespace.Secrets = secrets
		namespaces = append(namespaces, namespace)
	}
	description.Namespaces = namespaces
	return description, nil
}

func validOptionalJSON(encoded *json.RawMessage) bool {
	return encoded == nil || json.Valid(*encoded)
}

func cloneRawMessagePointer(source *json.RawMessage) *json.RawMessage {
	if source == nil {
		return nil
	}
	detached := append(json.RawMessage(nil), (*source)...)
	return &detached
}
