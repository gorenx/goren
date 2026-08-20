package agentloop

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	sesspersist "github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

// Loop is the concrete Agent Factory Service. Registry remains the ordinary
// consumer-facing creation boundary.
type Loop interface {
	plugin.Service
	agent.Factory
	Create(context.Context, session.SessionID, agent.Options, session.Metadata) (agent.Handle, error)
	Resume(context.Context, session.SessionID, agent.Options) (agent.Handle, error)
	StartConfigured(context.Context) ([]agent.Handle, error)
	MaxParallelToolCalls() int
}

// RuntimeOptions contains process-local failure reporting policy.
type RuntimeOptions struct {
	ObserverError func(error)
}

// LoopPlugin owns Agent construction and every live Agent activation tree.
// Concrete Agents resolve their exact scoped capabilities during Apply.
type LoopPlugin struct {
	plugin.Base
	config      ValidatedConfig
	reporter    func(error)
	agents      agent.Registry
	sessions    session.LiveStore
	persistence sesspersist.Persistence

	mutex             sync.Mutex
	accepting         bool
	configuredStarted bool
	live              map[*agentLifecycle]struct{}
}

// New constructs an inactive Agent Loop Plugin from validated configuration.
func New(
	settings ValidatedConfig,
	policies RuntimeOptions,
) (*LoopPlugin, error) {
	if settings.MaxParallelToolCalls() < 1 {
		return nil, errors.New("agentloop: configuration was not validated")
	}
	reporter := policies.ObserverError
	if reporter == nil {
		reporter = func(error) {}
	}
	return &LoopPlugin{
		config:   settings,
		reporter: reporter,
		live:     make(map[*agentLifecycle]struct{}),
	}, nil
}

// Manifest declares the factory Service and the root capabilities required by
// every Agent activation assembled below it.
func (*LoopPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName,
		Provides: []plugin.ServiceType{
			plugin.ServiceOf[Loop](),
		},
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[agent.Registry](),
			plugin.ServiceOf[session.LiveStore](),
			plugin.ServiceOf[llm.LlmRuntime](),
			plugin.ServiceOf[tools.ToolRuntime](),
			plugin.ServiceOf[systemprompt.Assembler](),
			plugin.ServiceOf[systemprompt.PromptRegistry](),
		},
		Optional: []plugin.ServiceType{
			plugin.ServiceOf[sesspersist.Persistence](),
		},
		Events: []plugin.EventSubscription{
			plugin.EventOf[session.SessionEventAppended](),
		},
	}
}

// Apply resolves root dependencies and attaches this exact Factory to Registry.
func (owner *LoopPlugin) Apply(requestContext context.Context) error {
	if err := requestContext.Err(); err != nil {
		return err
	}
	agents, err := plugin.Require[agent.Registry](owner)
	if err != nil {
		return err
	}
	sessions, err := plugin.Require[session.LiveStore](owner)
	if err != nil {
		return err
	}
	if _, err = plugin.Require[llm.LlmRuntime](owner); err != nil {
		return err
	}
	if _, err = plugin.Require[tools.ToolRuntime](owner); err != nil {
		return err
	}
	if _, err = plugin.Require[systemprompt.Assembler](owner); err != nil {
		return err
	}
	if _, err = plugin.Require[systemprompt.PromptRegistry](owner); err != nil {
		return err
	}
	durability, _ := plugin.Resolve[sesspersist.Persistence](owner)
	if err = agents.AttachFactory(owner); err != nil {
		return err
	}
	owner.mutex.Lock()
	owner.agents = agents
	owner.sessions = sessions
	owner.persistence = durability
	owner.accepting = true
	owner.mutex.Unlock()
	return requestContext.Err()
}

// Dispose closes construction after all child Agent activation trees stop.
func (owner *LoopPlugin) Dispose(context.Context) error {
	owner.mutex.Lock()
	owner.accepting = false
	agents := owner.agents
	dangling := len(owner.live)
	owner.agents = nil
	owner.sessions = nil
	owner.persistence = nil
	owner.mutex.Unlock()
	if agents != nil {
		agents.DetachFactory(owner)
	}
	if dangling != 0 {
		return fmt.Errorf(
			"agentloop: Loop stopped with %d live Agent activation(s)",
			dangling,
		)
	}
	return nil
}

// ObserveEvent routes committed Session events to the one live Agent that owns
// the exact Session. The Session Store remains the event publisher.
func (owner *LoopPlugin) ObserveEvent(
	_ context.Context,
	fact plugin.Event,
) error {
	appended, matches := fact.(session.SessionEventAppended)
	if !matches || appended.Conversation == nil {
		return nil
	}
	owner.mutex.Lock()
	agents := owner.agents
	owner.mutex.Unlock()
	if agents == nil {
		return nil
	}
	subject, found := agents.Get(appended.Conversation.ID())
	if !found || subject.SessionValue() != appended.Conversation {
		return nil
	}
	concrete, matches := subject.(*ReactLoopAgent)
	if matches && concrete.driver != nil {
		concrete.driver.acceptSessionEvent(appended.Committed)
	}
	return nil
}

// MaxParallelToolCalls returns the deployment-wide per-Agent scheduler cap.
func (owner *LoopPlugin) MaxParallelToolCalls() int {
	return owner.config.MaxParallelToolCalls()
}

// Create is the direct fresh-session entry point.
func (owner *LoopPlugin) Create(
	requestContext context.Context,
	identifier session.SessionID,
	loopOptions agent.Options,
	metadata session.Metadata,
) (agent.Handle, error) {
	return owner.CreateAgent(
		requestContext,
		agent.CreateOptions{
			SessionID:    identifier,
			Metadata:     metadata,
			AgentOptions: loopOptions,
		},
	)
}

// Resume is the direct durable-session entry point.
func (owner *LoopPlugin) Resume(
	requestContext context.Context,
	identifier session.SessionID,
	loopOptions agent.Options,
) (agent.Handle, error) {
	return owner.ResumeAgent(
		requestContext,
		agent.ResumeOptions{
			SessionID:    identifier,
			AgentOptions: loopOptions,
		},
	)
}

// StartConfigured starts boot declarations after Runtime.Start has made the
// Loop active. Dynamic child mounting is intentionally absent from Apply.
func (owner *LoopPlugin) StartConfigured(
	requestContext context.Context,
) ([]agent.Handle, error) {
	if requestContext == nil {
		return nil, errors.New("agentloop: configured startup Context is nil")
	}
	owner.mutex.Lock()
	if !owner.accepting {
		owner.mutex.Unlock()
		return nil, errors.New("agentloop: Agent Loop is not active")
	}
	if owner.configuredStarted {
		owner.mutex.Unlock()
		return nil, errors.New("agentloop: configured Agents were already started")
	}
	owner.configuredStarted = true
	owner.mutex.Unlock()

	started := make([]agent.Handle, 0, len(owner.config.ConfiguredAgents()))
	rollback := func(cause error) ([]agent.Handle, error) {
		for index := len(started) - 1; index >= 0; index-- {
			cause = errors.Join(cause, started[index].Dispose(requestContext))
		}
		return nil, cause
	}
	for _, declaration := range owner.config.ConfiguredAgents() {
		if declaration.ResumeSessionID != "" {
			handleState, err := owner.Resume(
				requestContext,
				declaration.ResumeSessionID,
				declaration.AgentOptions(),
			)
			if err != nil {
				return rollback(fmt.Errorf(
					"agentloop: resume configured Agent %q: %w",
					declaration.ID,
					err,
				))
			}
			started = append(started, handleState)
			continue
		}
		identifier := declaration.SessionID
		if identifier == "" {
			var err error
			identifier, err = newConfiguredSessionID(declaration.ID)
			if err != nil {
				return rollback(err)
			}
		}
		metadata := session.Metadata{}
		if declaration.CWD != "" {
			workingDirectory := declaration.CWD
			metadata.CWD = &workingDirectory
		}
		handleState, err := owner.Create(
			requestContext,
			identifier,
			declaration.AgentOptions(),
			metadata,
		)
		if err != nil {
			return rollback(fmt.Errorf(
				"agentloop: start configured Agent %q: %w",
				declaration.ID,
				err,
			))
		}
		started = append(started, handleState)
	}
	return started, nil
}

// CreateAgent prepares unpublished state and then activates one scoped Agent
// Plugin tree before either Registry announces it.
func (owner *LoopPlugin) CreateAgent(
	requestContext context.Context,
	settings agent.CreateOptions,
) (agent.Handle, error) {
	if requestContext == nil {
		return agent.Handle{}, errors.New("agentloop: creation Context is nil")
	}
	sessions, _, err := owner.constructionServices()
	if err != nil {
		return agent.Handle{}, err
	}
	if settings.SessionID == "" {
		return agent.Handle{}, errors.New("agentloop: Agent Session id is empty")
	}
	if err := validateAgentOptions(settings.AgentOptions); err != nil {
		return agent.Handle{}, err
	}
	identifier := settings.SessionID
	conversation, err := sessions.Prepare(
		&identifier,
		session.CreateOptions{
			Seed:     settings.Seed,
			Metadata: settings.Metadata,
		},
	)
	if err != nil {
		return agent.Handle{}, err
	}
	return owner.activate(
		requestContext,
		conversation,
		settings.AgentOptions,
		settings.Extensions,
		agent.SessionStartup,
	)
}

// ResumeAgent prepares one exact durable Session before activating its Agent.
func (owner *LoopPlugin) ResumeAgent(
	requestContext context.Context,
	settings agent.ResumeOptions,
) (agent.Handle, error) {
	if requestContext == nil {
		return agent.Handle{}, errors.New("agentloop: resume Context is nil")
	}
	_, durability, err := owner.constructionServices()
	if err != nil {
		return agent.Handle{}, err
	}
	if settings.SessionID == "" {
		return agent.Handle{}, errors.New("agentloop: resume Session id is empty")
	}
	if err := validateAgentOptions(settings.AgentOptions); err != nil {
		return agent.Handle{}, err
	}
	if durability == nil {
		return agent.Handle{}, errors.New(
			"agentloop: session persistence is not configured",
		)
	}
	prepared, err := durability.Prepare(requestContext, settings.SessionID)
	if err != nil {
		return agent.Handle{}, err
	}
	defer prepared.Dispose()
	return owner.activate(
		requestContext,
		prepared.UnpublishedSession(),
		settings.AgentOptions,
		settings.Extensions,
		agent.SessionResume,
	)
}

func (owner *LoopPlugin) activate(
	requestContext context.Context,
	conversation *session.Session,
	loopOptions agent.Options,
	extensions []plugin.Plugin,
	startSource agent.SessionStartSource,
) (agent.Handle, error) {
	if conversation == nil {
		return agent.Handle{}, errors.New("agentloop: prepared Session is nil")
	}
	subject, err := newReactLoopAgent(owner, conversation, loopOptions)
	if err != nil {
		return agent.Handle{}, err
	}
	lifecycle := newAgentLifecycle(owner)
	subject.lifecycle = lifecycle
	initiator, _ := agent.InitiatorFrom(requestContext)
	membership := newAgentMembership(
		owner,
		lifecycle,
		subject,
		startSource,
		initiator,
	)
	subject.children = []plugin.ChildPlugin{
		{
			Instance:  systemprompt.NewOverlay(systemprompt.RegistryOptions{}),
			Placement: plugin.SameScope,
			Phase:     plugin.ActivationMain,
		},
		{
			Instance:  tools.NewOverlay(),
			Placement: plugin.SameScope,
			Phase:     plugin.ActivationMain,
		},
		{
			Instance:  newAgentVariables(loopOptions, conversation.Header()),
			Placement: plugin.SameScope,
			Phase:     plugin.ActivationMain,
		},
	}
	for _, extension := range extensions {
		subject.children = append(subject.children, plugin.ChildPlugin{
			Instance:  extension,
			Placement: plugin.SameScope,
			Phase:     plugin.ActivationMain,
		})
	}
	subject.children = append(subject.children, plugin.ChildPlugin{
		Instance:  membership,
		Placement: plugin.SameScope,
		Phase:     plugin.ActivationCommit,
	})
	rootHandle, err := plugin.MountScopedChild(
		requestContext,
		owner,
		subject,
	)
	if err != nil {
		return agent.Handle{}, err
	}
	if !lifecycle.attachRoot(rootHandle) {
		return agent.Handle{}, errors.Join(
			errors.New("agentloop: Agent tree stopped before Handle attachment"),
			lifecycle.Dispose(requestContext),
		)
	}
	return agent.NewHandle(subject, lifecycle)
}

func validateAgentOptions(loopOptions agent.Options) error {
	if loopOptions.MaxTokens != nil &&
		(*loopOptions.MaxTokens <= 0 ||
			int64(*loopOptions.MaxTokens) > maxSafeInteger) {
		return errors.New(
			"agentloop: Agent maxTokens must be a positive safe integer",
		)
	}
	return nil
}

func (owner *LoopPlugin) constructionServices() (
	session.LiveStore,
	sesspersist.Persistence,
	error,
) {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	if !owner.accepting || owner.agents == nil || owner.sessions == nil {
		return nil, nil, errors.New("agentloop: Agent Loop is not active")
	}
	return owner.sessions, owner.persistence, nil
}

func (owner *LoopPlugin) track(lifecycle *agentLifecycle) error {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	if !owner.accepting {
		return errors.New("agentloop: Agent Loop is not active")
	}
	owner.live[lifecycle] = struct{}{}
	return nil
}

func (owner *LoopPlugin) forget(lifecycle *agentLifecycle) {
	owner.mutex.Lock()
	delete(owner.live, lifecycle)
	owner.mutex.Unlock()
}

func (owner *LoopPlugin) report(problem error) {
	defer func() { _ = recover() }()
	owner.reporter(problem)
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
	return session.SessionID(
		prefix + "-session-" + string(encoded),
	), nil
}
