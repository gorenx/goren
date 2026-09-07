package titleactor

import (
	"context"

	"github.com/gorenx/goren/session/title"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// worker executes model I/O independently of the Title command mailbox.
// Its framework mailbox orders drain after every preceding generation.
type worker struct{}

type generate struct {
	requestContext context.Context
	call           title.Generation
}

type generated struct {
	result title.ProviderResult
	err    error
}

type drain struct{}

func (*worker) PreStart(*goakt.Context) error { return nil }
func (*worker) PostStop(*goakt.Context) error { return nil }
func (*worker) Receive(rc *goakt.ReceiveContext) {
	switch input := rc.Message().(type) {
	case generate:
		if err := input.requestContext.Err(); err != nil {
			rc.Response(generated{err: context.Cause(input.requestContext)})
			return
		}
		result, err := input.call.Generate(input.requestContext)
		if input.requestContext.Err() != nil {
			err = context.Cause(input.requestContext)
		}
		rc.Response(generated{
			result: result,
			err:    err,
		})
	case drain:
		rc.Response(Completion{})
	default:
		rc.Unhandled()
	}
}
