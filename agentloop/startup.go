package agentloop

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
)

// configuredAgentStarter owns the one-shot boot transaction for declarative
// Agent entries. A failed transaction is terminal for this Loop instance; the
// composition root must stop the Runtime rather than retry partial startup.
type configuredAgentStarter struct {
	mutex        sync.Mutex
	declarations []StartupAgent
	started      bool
}

func newConfiguredAgentStarter(
	declarations []StartupAgent,
) *configuredAgentStarter {
	return &configuredAgentStarter{
		declarations: declarations,
	}
}

func (starter *configuredAgentStarter) start(
	requestContext context.Context,
	factory *Plugin,
) ([]agent.Handle, error) {
	if requestContext == nil {
		return nil, errors.New("agentloop: configured startup Context is nil")
	}
	if factory == nil {
		return nil, errors.New("agentloop: configured startup Factory is nil")
	}
	if err := factory.admission.requireOpen(); err != nil {
		return nil, err
	}
	starter.mutex.Lock()
	if starter.started {
		starter.mutex.Unlock()
		return nil, errors.New(
			"agentloop: configured Agents were already started",
		)
	}
	starter.started = true
	declarations := starter.declarations
	starter.declarations = nil
	starter.mutex.Unlock()

	started := make([]agent.Handle, 0, len(declarations))
	rollback := func(cause error) ([]agent.Handle, error) {
		rollbackContext := context.WithoutCancel(requestContext)
		for index := len(started) - 1; index >= 0; index-- {
			cause = errors.Join(
				cause,
				started[index].Dispose(rollbackContext),
			)
		}
		return nil, cause
	}
	for _, declaration := range declarations {
		if declaration.Resume {
			handleState, err := factory.ResumeAgent(
				requestContext,
				agent.ResumeOptions{
					SessionID:    declaration.SessionID,
					AgentOptions: declaration.AgentOptions,
				},
			)
			if err != nil {
				return rollback(fmt.Errorf(
					"agentloop: resume configured Agent %q: %w",
					declaration.Label,
					err,
				))
			}
			started = append(started, handleState)
			continue
		}
		identifier := declaration.SessionID
		if identifier == "" {
			var err error
			identifier, err = newConfiguredSessionID(declaration.Label)
			if err != nil {
				return rollback(err)
			}
		}
		handleState, err := factory.CreateAgent(
			requestContext,
			agent.CreateOptions{
				SessionID:    identifier,
				Metadata:     cloneSessionMetadata(declaration.Metadata),
				AgentOptions: cloneAgentOptions(declaration.AgentOptions),
			},
		)
		if err != nil {
			return rollback(fmt.Errorf(
				"agentloop: start configured Agent %q: %w",
				declaration.Label,
				err,
			))
		}
		started = append(started, handleState)
	}
	return started, nil
}

func newConfiguredSessionID(prefix string) (session.SessionID, error) {
	var randomBytes [16]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		return "", fmt.Errorf(
			"agentloop: generate configured Agent id: %w",
			err,
		)
	}
	randomBytes[6] = randomBytes[6]&0x0f | 0x40
	randomBytes[8] = randomBytes[8]&0x3f | 0x80
	encoded := make([]byte, 36)
	hex.Encode(encoded[0:8], randomBytes[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], randomBytes[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], randomBytes[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], randomBytes[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], randomBytes[10:16])
	return session.SessionID(
		prefix + "-session-" + string(encoded),
	), nil
}
