package sessionapi

import (
	"context"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
)

func TestGatewayAdmissionRecoversHistoryAndRejectsLiveChild(t *testing.T) {
	header := session.Header{
		ID:     "conversation",
		Origin: session.OriginSubagent,
	}
	directory := &activationDirectory{
		created: activationAgent{
			header: header,
		},
	}
	owner := &Gateway{
		lifecycle: &sessionLifecycle{
			access: &sessionAccess{
				runtimeSessions: &activation{
					agents: directory,
					history: activationHistory{
						header: header,
					},
				},
			},
		},
	}
	if refused := owner.ActivateAgent(context.Background(), header.ID); refused != nil {
		t.Fatal(refused)
	}
	if !directory.opening.Restore || directory.opening.ID != header.ID {
		t.Fatalf("durable Session not recovered: %#v", directory.opening)
	}
	parent := session.SessionID("parent")
	directory.current = activationAgent{
		header: header,
		parent: &parent,
	}
	if refused := owner.ActivateAgent(context.Background(), header.ID); refused == nil || refused.Code != connection.ErrorAgentBusy {
		t.Fatalf("live child admitted: %#v", refused)
	}
	directory.current = activationAgent{
		header: header,
		parent: &parent,
		role:   agent.CompanionRole,
	}
	if refused := owner.ActivateAgent(context.Background(), header.ID); refused != nil {
		t.Fatalf("live Companion refused: %#v", refused)
	}
	directory.current = nil
	owner.lifecycle.access.runtimeSessions.history = activationHistory{
		failure: &persistence.NotFoundError{
			ID: header.ID,
		},
	}
	if refused := owner.ActivateAgent(context.Background(), header.ID); refused == nil || refused.Code != connection.ErrorSessionNotFound {
		t.Fatalf("missing Session: %#v", refused)
	}
}
