package assembly

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/gorenx/goren/plugin"
	webui "github.com/gorenx/goren/web"
)

type webFrontend interface {
	http.Handler
}

var webFrontendServiceKey = plugin.DefineService[webFrontend]("webFrontend")

type WebConfig struct{}

type webFrontendFactory struct{}

func (webFrontendFactory) Name() string { return WebFrontendFactoryName }

func (webFrontendFactory) DecodeConfig(rawConfig json.RawMessage) (WebConfig, error) {
	return plugin.DecodeStrictConfig[WebConfig](rawConfig, nil)
}

func (webFrontendFactory) New(context.Context, WebConfig) (plugin.Plugin, error) {
	return &webFrontendPlugin{}, nil
}

type webFrontendPlugin struct{}

func (*webFrontendPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{Name: WebFrontendFactoryName, Provides: []plugin.ServiceRef{webFrontendServiceKey.Ref()}}
}

func (*webFrontendPlugin) Apply(_ context.Context, pluginScope *plugin.Scope) error {
	browserAssets := webui.New()
	_, err := plugin.Provide(pluginScope, webFrontendServiceKey, webFrontend(browserAssets))
	return err
}
