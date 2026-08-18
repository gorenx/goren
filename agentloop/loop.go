package agentloop

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	sesspersist "github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

// Loop is the concrete factory service exposed under the source agentLoop key.
// Registry remains the ordinary consumer-facing creation entry point.
type Loop interface {
	agent.Factory
	Create(context.Context, *plugin.Scope, session.SessionID, agent.Options, session.Metadata) (agent.Handle, error)
	Resume(context.Context, *plugin.Scope, session.SessionID, agent.Options) (agent.Handle, error)
	MaxParallelToolCalls() int
}

// Service is the canonical Agent Loop Service Definition.
var Service = plugin.DefineService[Loop](ServiceName)

// Dependencies are the source Agent Loop's five required services.
type Dependencies struct {
	Agents       agent.Registry
	Sessions     session.LiveStore
	LLM          llm.LlmRuntime
	Tools        tools.ToolRuntime
	SystemPrompt systemprompt.SystemPrompt
}

// RuntimeOptions contains process-local failure reporting policy.
type RuntimeOptions struct {
	ObserverError func(error)
}

type loopService struct {
	sourceScope *plugin.Scope
	agents      agent.Registry
	sessions    session.LiveStore
	llm         llm.LlmRuntime
	tools       tools.ToolRuntime
	prompts     systemprompt.SystemPrompt
	config      ValidatedConfig
	reporter    func(error)
	nextScope   atomic.Uint64

	mu        sync.Mutex
	accepting bool
	live      map[*agentLifecycle]struct{}
}

// New constructs the factory, registers it with agents, and starts configured Agents.
func New(
	requestContext context.Context,
	sourceScope *plugin.Scope,
	ports Dependencies,
	settings ValidatedConfig,
	policies RuntimeOptions,
) (Loop, error) {
	if requestContext == nil || sourceScope == nil {
		return nil, errors.New("agentloop: Context and source Scope are required")
	}
	if ports.Agents == nil || ports.Sessions == nil || ports.LLM == nil || ports.Tools == nil || ports.SystemPrompt == nil {
		return nil, errors.New("agentloop: agents, sessions, llm, tools, and systemPrompt are required")
	}
	reporter := policies.ObserverError
	if reporter == nil {
		reporter = func(error) {}
	}
	owner := &loopService{
		sourceScope: sourceScope,
		agents:      ports.Agents,
		sessions:    ports.Sessions,
		llm:         ports.LLM,
		tools:       ports.Tools,
		prompts:     ports.SystemPrompt,
		config:      settings,
		reporter:    reporter,
		accepting:   true,
		live:        make(map[*agentLifecycle]struct{}),
	}
	err := sourceScope.Effect(
		requestContext,
		"agentLoop.transactions()",
		func(context.Context) (plugin.Disposer, error) {
			return owner.close, nil
		},
	)
	if err != nil {
		return nil, err
	}
	if _, err = ports.Agents.SetFactory(requestContext, sourceScope, owner); err != nil {
		return nil, err
	}
	for _, declaration := range settings.ConfiguredAgents() {
		if declaration.ResumeSessionID != "" {
			if _, err = owner.Resume(
				requestContext, sourceScope, declaration.ResumeSessionID, declaration.AgentOptions(),
			); err != nil {
				return nil, fmt.Errorf("agentloop: resume configured agent %q: %w", declaration.ID, err)
			}
			continue
		}
		identifier := declaration.SessionID
		if identifier == "" {
			var err error
			identifier, err = newConfiguredSessionID(declaration.ID)
			if err != nil {
				return nil, err
			}
		}
		metadata := session.Metadata{}
		if declaration.CWD != "" {
			workingDirectory := declaration.CWD
			metadata.CWD = &workingDirectory
		}
		_, err = owner.Create(requestContext, sourceScope, identifier, declaration.AgentOptions(), metadata)
		if err != nil {
			return nil, fmt.Errorf("agentloop: start configured agent %q: %w", declaration.ID, err)
		}
	}
	return owner, nil
}

func (owner *loopService) MaxParallelToolCalls() int {
	return owner.config.MaxParallelToolCalls()
}

// Create is the direct source-compatible fresh-session entry point.
func (owner *loopService) Create(
	requestContext context.Context,
	ownerScope *plugin.Scope,
	identifier session.SessionID,
	loopOptions agent.Options,
	metadata session.Metadata,
) (agent.Handle, error) {
	return owner.CreateAgent(
		requestContext,
		ownerScope,
		agent.CreateOptions{
			SessionID:    identifier,
			Metadata:     metadata,
			AgentOptions: loopOptions,
		},
	)
}

// Resume is the direct source-compatible durable-session entry point.
func (owner *loopService) Resume(
	requestContext context.Context,
	ownerScope *plugin.Scope,
	identifier session.SessionID,
	loopOptions agent.Options,
) (agent.Handle, error) {
	return owner.ResumeAgent(
		requestContext,
		ownerScope,
		agent.ResumeOptions{
			SessionID:    identifier,
			AgentOptions: loopOptions,
		},
	)
}

// CreateAgent prepares unpublished resources, applies setup, then publishes Session and Agent in order.
func (owner *loopService) CreateAgent(
	requestContext context.Context,
	ownerScope *plugin.Scope,
	createOptions agent.CreateOptions,
) (agent.Handle, error) {
	if requestContext == nil || ownerScope == nil {
		return agent.Handle{}, errors.New("agentloop: creation Context and owner Scope are required")
	}
	if err := owner.assertAccepting(); err != nil {
		return agent.Handle{}, err
	}
	if createOptions.SessionID == "" {
		return agent.Handle{}, errors.New("agentloop: Agent Session id is empty")
	}
	if err := validateAgentOptions(createOptions.AgentOptions); err != nil {
		return agent.Handle{}, err
	}
	identifier := createOptions.SessionID
	conversation, err := owner.sessions.Prepare(&identifier, session.CreateOptions{
		Seed: createOptions.Seed, Metadata: createOptions.Metadata,
	})
	if err != nil {
		return agent.Handle{}, err
	}
	return owner.setupAndPublish(
		requestContext,
		ownerScope,
		conversation,
		createOptions.AgentOptions,
		createOptions.Setup,
		agent.SessionStartup,
	)
}

// ResumeAgent loads one exact unpublished persisted Session before publication.
func (owner *loopService) ResumeAgent(
	requestContext context.Context,
	ownerScope *plugin.Scope,
	resumeOptions agent.ResumeOptions,
) (agent.Handle, error) {
	if requestContext == nil || ownerScope == nil {
		return agent.Handle{}, errors.New("agentloop: resume Context and owner Scope are required")
	}
	if err := owner.assertAccepting(); err != nil {
		return agent.Handle{}, err
	}
	if resumeOptions.SessionID == "" {
		return agent.Handle{}, errors.New("agentloop: resume Session id is empty")
	}
	if err := validateAgentOptions(resumeOptions.AgentOptions); err != nil {
		return agent.Handle{}, err
	}
	durability, found := plugin.Require(owner.sourceScope, sesspersist.Service)
	if !found {
		return agent.Handle{}, errors.New("agentloop: session persistence is not configured")
	}
	prepared, err := durability.Prepare(requestContext, resumeOptions.SessionID)
	if err != nil {
		return agent.Handle{}, err
	}
	defer prepared.Dispose()
	return owner.setupAndPublish(
		requestContext, ownerScope, prepared.UnpublishedSession(), resumeOptions.AgentOptions, resumeOptions.Setup, agent.SessionResume,
	)
}

func (owner *loopService) setupAndPublish(
	requestContext context.Context,
	ownerScope *plugin.Scope,
	conversation *session.Session,
	loopOptions agent.Options,
	setup agent.Setup,
	startSource agent.SessionStartSource,
) (agent.Handle, error) {
	childLabel := fmt.Sprintf("agent-%d", owner.nextScope.Add(1))
	agentScope, releaseScope, err := owner.sourceScope.Child(childLabel)
	if err != nil {
		return agent.Handle{}, err
	}
	subject, err := newReactLoopAgent(owner, conversation, loopOptions, agentScope)
	if err != nil {
		return agent.Handle{}, errors.Join(err, releaseScope(requestContext))
	}
	lifecycle := &agentLifecycle{
		owner: owner, subject: subject, releaseScope: releaseScope, done: make(chan struct{}), active: true,
	}
	owner.mu.Lock()
	if !owner.accepting {
		owner.mu.Unlock()
		return agent.Handle{}, errors.Join(errors.New("agentloop: Agent Loop is not active"), lifecycle.close(requestContext))
	}
	owner.live[lifecycle] = struct{}{}
	owner.mu.Unlock()
	ownedRelease, err := plugin.Own(ownerScope, "agentLoop.lifecycle("+string(conversation.ID())+")", lifecycle.close)
	if err != nil {
		return agent.Handle{}, errors.Join(err, lifecycle.close(requestContext))
	}
	lifecycle.ownerRelease = ownedRelease

	fail := func(cause error) (agent.Handle, error) {
		return agent.Handle{}, errors.Join(cause, ownedRelease(requestContext))
	}
	if err = owner.installVariables(requestContext, agentScope, subject); err != nil {
		return fail(err)
	}
	if setup != nil {
		commit, setupErr := setup.Apply(requestContext, agentScope)
		if setupErr != nil {
			return fail(setupErr)
		}
		if commit != nil {
			if commitErr := commit.Commit(); commitErr != nil {
				return fail(commitErr)
			}
		}
	}
	if err = requestContext.Err(); err != nil {
		return fail(err)
	}
	detachSession, err := owner.sessions.Enter(conversation)
	if err != nil {
		return fail(err)
	}
	if err = lifecycle.attachSession(requestContext, detachSession); err != nil {
		return fail(err)
	}
	initiator, _ := agent.InitiatorFrom(requestContext)
	detachAgent, err := owner.agents.Enter(subject, initiator)
	if err != nil {
		return fail(err)
	}
	if err = lifecycle.attachAgent(requestContext, detachAgent); err != nil {
		return fail(err)
	}
	if err = owner.sessions.Announce(requestContext, conversation); err != nil {
		return fail(err)
	}
	if err = lifecycle.assertActive(); err != nil {
		return fail(err)
	}
	if err = owner.agents.Announce(requestContext, subject); err != nil {
		return fail(err)
	}
	if err = lifecycle.assertActive(); err != nil {
		return fail(err)
	}
	if observerErr := agent.EmitSessionStart(requestContext, owner.sourceScope, subject, startSource); observerErr != nil {
		owner.report(fmt.Errorf("agentloop: Agent %q session-start observer: %w", subject.ID(), observerErr))
	}
	if err = lifecycle.assertActive(); err != nil {
		return fail(err)
	}
	return agent.Handle{Subject: subject, Release: ownedRelease}, nil
}

func validateAgentOptions(loopOptions agent.Options) error {
	if loopOptions.MaxTokens != nil &&
		(*loopOptions.MaxTokens <= 0 || int64(*loopOptions.MaxTokens) > maxSafeInteger) {
		return errors.New("agentloop: Agent maxTokens must be a positive safe integer")
	}
	return nil
}

func (owner *loopService) installVariables(requestContext context.Context, agentScope *plugin.Scope, subject *ReactLoopAgent) error {
	loopOptions := subject.OptionsValue()
	headerSnapshot := subject.SessionValue().Header()
	providers := []struct {
		name  string
		value string
	}{
		{name: "provider", value: loopOptions.Provider},
		{name: "model", value: loopOptions.Model},
	}
	if headerSnapshot.CWD == nil {
		providers = append(providers, struct {
			name  string
			value string
		}{name: "cwd"})
	} else {
		providers = append(providers, struct {
			name  string
			value string
		}{name: "cwd", value: *headerSnapshot.CWD})
	}
	for _, entry := range providers {
		retained := entry.value
		if _, err := owner.prompts.Variable(requestContext, agentScope, entry.name,
			func(context.Context, systemprompt.AssembleContext) (systemprompt.VariableValue, error) {
				return systemprompt.VariableValue{Value: retained, Defined: retained != ""}, nil
			}); err != nil {
			return err
		}
	}
	return nil
}

func (owner *loopService) assertAccepting() error {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if !owner.accepting {
		return errors.New("agentloop: Agent Loop is not active")
	}
	return nil
}

func (owner *loopService) close(closeContext context.Context) error {
	owner.mu.Lock()
	if !owner.accepting && len(owner.live) == 0 {
		owner.mu.Unlock()
		return nil
	}
	owner.accepting = false
	lifecycles := make([]*agentLifecycle, 0, len(owner.live))
	for lifecycle := range owner.live {
		lifecycles = append(lifecycles, lifecycle)
	}
	owner.mu.Unlock()
	var closeErr error
	for _, lifecycle := range lifecycles {
		closeErr = errors.Join(closeErr, lifecycle.close(closeContext))
	}
	return closeErr
}

func (owner *loopService) forget(lifecycle *agentLifecycle) {
	owner.mu.Lock()
	delete(owner.live, lifecycle)
	owner.mu.Unlock()
}

func (owner *loopService) report(problem error) {
	defer func() { _ = recover() }()
	owner.reporter(problem)
}

type agentLifecycle struct {
	owner        *loopService
	subject      *ReactLoopAgent
	releaseScope plugin.Disposer
	ownerRelease plugin.Disposer

	mu            sync.Mutex
	active        bool
	closing       bool
	done          chan struct{}
	closeErr      error
	detachAgent   plugin.Disposer
	detachSession plugin.Disposer
}

func (lifecycle *agentLifecycle) attachAgent(requestContext context.Context, release plugin.Disposer) error {
	lifecycle.mu.Lock()
	if !lifecycle.active {
		lifecycle.mu.Unlock()
		return errors.Join(
			fmt.Errorf("agentloop: Agent %q lifecycle was disposed during publication", lifecycle.subject.ID()),
			release(requestContext),
		)
	}
	lifecycle.detachAgent = release
	lifecycle.mu.Unlock()
	return nil
}

func (lifecycle *agentLifecycle) attachSession(requestContext context.Context, release plugin.Disposer) error {
	lifecycle.mu.Lock()
	if !lifecycle.active {
		lifecycle.mu.Unlock()
		return errors.Join(
			fmt.Errorf("agentloop: Agent %q lifecycle was disposed during publication", lifecycle.subject.ID()),
			release(requestContext),
		)
	}
	lifecycle.detachSession = release
	lifecycle.mu.Unlock()
	return nil
}

func (lifecycle *agentLifecycle) assertActive() error {
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	if !lifecycle.active {
		return fmt.Errorf("agentloop: Agent %q lifecycle was disposed during publication", lifecycle.subject.ID())
	}
	return nil
}

func (lifecycle *agentLifecycle) close(closeContext context.Context) error {
	lifecycle.mu.Lock()
	if lifecycle.closing {
		done := lifecycle.done
		lifecycle.mu.Unlock()
		<-done
		lifecycle.mu.Lock()
		closeErr := lifecycle.closeErr
		lifecycle.mu.Unlock()
		return closeErr
	}
	lifecycle.closing = true
	lifecycle.active = false
	detachAgent := lifecycle.detachAgent
	detachSession := lifecycle.detachSession
	lifecycle.mu.Unlock()

	lifecycle.subject.beginDispose()
	quiescenceErr := lifecycle.subject.WhenIdle(context.Background())
	if closeContext == nil {
		closeContext = context.Background()
	}
	closeErr := errors.Join(quiescenceErr, lifecycle.releaseScope(closeContext))
	if detachAgent != nil {
		closeErr = errors.Join(closeErr, detachAgent(closeContext))
	}
	if detachSession != nil {
		closeErr = errors.Join(closeErr, detachSession(closeContext))
	}
	lifecycle.owner.forget(lifecycle)
	lifecycle.mu.Lock()
	lifecycle.closeErr = closeErr
	close(lifecycle.done)
	lifecycle.mu.Unlock()
	return closeErr
}

func newConfiguredSessionID(prefix string) (session.SessionID, error) {
	var randomBytes [16]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		return "", fmt.Errorf("agentloop: generate configured Agent id: %w", err)
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
	return session.SessionID(prefix + "-session-" + string(encoded)), nil
}
