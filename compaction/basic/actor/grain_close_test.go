package compactionactor

import (
	"testing"

	middlewareactor "github.com/gorenx/goren/agent/middleware/actor"
	"github.com/gorenx/goren/compaction/basic"
	goakt "github.com/tochemey/goakt/v4/actor"
)

type closeReply struct {
	value   Result
	failure error
}

func (target *closeReply) Response(payload any) { target.value = payload.(Result) }
func (target *closeReply) Err(failure error)    { target.failure = failure }

func TestClosingRejectsNewCompactionWork(t *testing.T) {
	owner := &Grain{
		business: &basic.Compaction{},
		agentID:  "conversation",
		command: &commandRegistration{
			phase: registrationClosing,
		},
	}
	// Routes must not be used once withdrawal starts. A nil context makes any
	// accidental attempt to schedule work fail instead of hiding it in a fixture.
	address := &goakt.GrainIdentity{}
	for _, mode := range []basic.Mode{basic.Manual, basic.Pressure, basic.Overflow} {
		reply := &closeReply{}
		owner.execute(nil, Execute{
			Agent:     address,
			RequestID: "request",
			Mode:      mode,
		}, reply, "")
		if reply.value.Err == nil {
			t.Fatalf("mode %v admitted work during withdrawal", mode)
		}
		if owner.active != nil {
			t.Fatal("rejected request acquired invocation ownership")
		}
	}
	if err := owner.admitHook(middlewareactor.Invocation{
		CallID:      "call",
		Handler:     "handler",
		Coordinator: address,
	}, "conversation"); err == nil {
		t.Fatal("middleware admitted work during withdrawal")
	}
}
