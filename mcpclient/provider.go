package mcpclient

import (
	"context"

	"github.com/gorenx/goren/tools"
	"github.com/gorenx/goren/tools/tool"
)

type provider struct {
	connection *Connection
	name       string
}

var _ tool.IdentifiedProvider = (*provider)(nil)

func (source *provider) ProviderID() tools.ProviderID { return source.connection.ProviderID() }

func (source *provider) Open(ctx context.Context) (tool.Implementation, error) {
	return source.connection.openTool(ctx, source.name)
}
