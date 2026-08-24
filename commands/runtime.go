package commands

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
)

var commandNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

const maxSafeInteger int64 = 1<<53 - 1

// RuntimeOptions supplies process identity and contained lifecycle diagnostics.
type RuntimeOptions struct {
	InstanceToken string
	ObserverError func(error)
}

type registeredCommand struct {
	specification Definition
	descriptor    Descriptor

	mutex     sync.Mutex
	active    int
	closed    bool
	quiescent chan struct{}
}

type handlerOutcome struct {
	result Result
	err    error
}

// ParsedLine is one syntactically valid slash command before registry lookup.
type ParsedLine struct {
	Name     string
	RawInput string
}

// CommandRuntime is the stateful Commands registry and executor.
type CommandRuntime struct {
	mutex    sync.RWMutex
	entries  map[string]*registeredCommand
	instance string
	sequence atomic.Uint64
	report   func(error)
}

// NewCommandRuntime constructs an empty Commands registry.
func NewCommandRuntime(options RuntimeOptions) (*CommandRuntime, error) {
	instanceToken := options.InstanceToken
	if instanceToken == "" {
		var randomBytes [4]byte
		if _, err := rand.Read(randomBytes[:]); err != nil {
			return nil, fmt.Errorf("commands: mint instance token: %w", err)
		}
		instanceToken = hex.EncodeToString(randomBytes[:])
	}
	if strings.TrimSpace(instanceToken) == "" || instanceToken != strings.TrimSpace(instanceToken) {
		return nil, errors.New("commands: instance token must be non-empty and trimmed")
	}
	reporter := options.ObserverError
	if reporter == nil {
		reporter = func(error) {}
	}
	return &CommandRuntime{
		entries:  make(map[string]*registeredCommand),
		instance: instanceToken,
		report:   reporter,
	}, nil
}

// ParseCommand parses the exact leading slash syntax without normalizing the
// trailing input.
func ParseCommand(line string) (ParsedLine, bool) {
	if len(line) < 2 || line[0] != '/' {
		return ParsedLine{}, false
	}
	nameEnd := 1
	for nameEnd < len(line) && !isCommandSeparator(line[nameEnd]) {
		nameEnd++
	}
	nameValue := line[1:nameEnd]
	if !commandNamePattern.MatchString(nameValue) {
		return ParsedLine{}, false
	}
	return ParsedLine{
		Name:     nameValue,
		RawInput: line[nameEnd:],
	}, true
}

func isCommandSeparator(value byte) bool {
	return value == ' ' || value == '\t' || value == '\n' || value == '\r'
}

// Register adds one global command definition and returns its exact lifecycle
// handle.
func (owner *CommandRuntime) Register(specification Definition) (*Registration, error) {
	entry, err := normalizeDefinition(specification)
	if err != nil {
		return nil, err
	}
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	if _, exists := owner.entries[entry.specification.Name]; exists {
		return nil, fmt.Errorf(
			"commands: command %q is already registered",
			entry.specification.Name,
		)
	}
	owner.entries[entry.specification.Name] = entry
	return &Registration{
		owner: owner,
		entry: entry,
	}, nil
}

// List returns name-sorted detached descriptors for the exact target Agent.
func (owner *CommandRuntime) List(subject agent.Agent) []Descriptor {
	if subject == nil {
		return []Descriptor{}
	}
	owner.mutex.RLock()
	descriptors := make([]Descriptor, 0, len(owner.entries))
	for _, entry := range owner.entries {
		descriptors = append(descriptors, cloneDescriptor(entry.descriptor))
	}
	owner.mutex.RUnlock()
	sort.Slice(descriptors, func(leftIndex int, rightIndex int) bool {
		return descriptors[leftIndex].Name < descriptors[rightIndex].Name
	})
	return descriptors
}

// Find resolves one effective global definition for the target Agent.
func (owner *CommandRuntime) Find(subject agent.Agent, nameValue string) (Definition, bool) {
	if subject == nil {
		return Definition{}, false
	}
	owner.mutex.RLock()
	entry := owner.entries[nameValue]
	owner.mutex.RUnlock()
	if entry == nil {
		return Definition{}, false
	}
	return cloneDefinition(entry.specification), true
}

// Execute parses and runs a registered command without sending it to the model.
func (owner *CommandRuntime) Execute(
	requestContext context.Context,
	subject agent.Agent,
	line string,
	options ExecuteOptions,
) (*Execution, error) {
	if requestContext == nil {
		return nil, errors.New("commands: Context is nil")
	}
	if subject == nil || subject.SessionValue() == nil {
		return nil, errors.New("commands: Agent and Session are required")
	}
	if options.AttachmentCount < 0 {
		return nil, errors.New("commands: attachment count is negative")
	}
	parsedValue, matched := ParseCommand(line)
	if !matched {
		return nil, nil
	}
	if err := requestContext.Err(); err != nil {
		return nil, err
	}
	owner.mutex.RLock()
	entry := owner.entries[parsedValue.Name]
	if entry == nil || !entry.begin() {
		owner.mutex.RUnlock()
		return nil, nil
	}
	owner.mutex.RUnlock()

	identifier := owner.mintCommandID()
	runValue := Run{
		CommandID: identifier,
		Name:      parsedValue.Name,
		Source: Source{
			Kind: "user",
		},
	}
	if entry.specification.RecordInput == nil || *entry.specification.RecordInput {
		rawInput := parsedValue.RawInput
		runValue.Args = &rawInput
	}
	if _, err := session.AppendSerialized(
		subject.SessionValue(),
		RunEvent,
		runValue,
	); err != nil {
		entry.end()
		return nil, err
	}
	if options.AttachmentCount > 0 &&
		(entry.specification.Input == nil || !entry.specification.Input.Images) {
		message := fmt.Sprintf("/%s does not accept image attachments", parsedValue.Name)
		outcome := Result{
			Kind: ResultError,
			Text: &message,
		}
		entry.end()
		return owner.settle(subject.SessionValue(), identifier, outcome)
	}

	completed := make(chan handlerOutcome, 1)
	input := Invocation{
		CommandID: identifier,
		Agent:     subject,
		RawInput:  parsedValue.RawInput,
	}
	go func() {
		defer entry.end()
		outcome, handlerErr := invokeHandler(
			entry.specification.Handler,
			requestContext,
			input,
		)
		completed <- handlerOutcome{
			result: outcome,
			err:    handlerErr,
		}
	}()

	select {
	case operation := <-completed:
		if cancelErr := requestContext.Err(); cancelErr != nil {
			owner.settleThrown(
				subject.SessionValue(),
				parsedValue.Name,
				identifier,
				cancelErr,
			)
			return nil, cancelErr
		}
		if operation.err != nil {
			owner.settleThrown(subject.SessionValue(), parsedValue.Name, identifier, operation.err)
			return nil, operation.err
		}
		if err := validateResult(operation.result); err != nil {
			owner.settleThrown(subject.SessionValue(), parsedValue.Name, identifier, err)
			return nil, err
		}
		return owner.settle(subject.SessionValue(), identifier, operation.result)
	case <-requestContext.Done():
		cancelErr := requestContext.Err()
		owner.settleThrown(subject.SessionValue(), parsedValue.Name, identifier, cancelErr)
		return nil, cancelErr
	}
}

func invokeHandler(
	operation Handler,
	requestContext context.Context,
	input Invocation,
) (outcome Result, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("commands: handler panic: %v", recovered)
		}
	}()
	return operation(requestContext, input)
}

func (owner *CommandRuntime) settle(
	conversation *session.Session,
	identifier ID,
	outcome Result,
) (*Execution, error) {
	err := session.SerializeProducer(conversation, func() error {
		if validateErr := validateSourceEvent(conversation, outcome); validateErr != nil {
			return validateErr
		}
		_, appendErr := session.Append(conversation, DoneEvent, Done{
			CommandID:      identifier,
			Kind:           outcome.Kind,
			Text:           cloneString(outcome.Text),
			SourceEventSeq: cloneInt64(outcome.SourceEventSeq),
		})
		return appendErr
	})
	if err != nil {
		return nil, err
	}
	return &Execution{
		CommandID: identifier,
		Result:    cloneResult(outcome),
	}, nil
}

func (owner *CommandRuntime) settleThrown(
	conversation *session.Session,
	nameValue string,
	identifier ID,
	failure error,
) {
	message := failure.Error()
	if _, err := session.AppendSerialized(
		conversation,
		DoneEvent,
		Done{
			CommandID: identifier,
			Kind:      ResultError,
			Text:      &message,
		},
	); err != nil {
		owner.reportFailure(fmt.Errorf(
			"commands: command %q command/done append failed: %w",
			nameValue,
			err,
		))
	}
}

func (owner *CommandRuntime) reportFailure(problem error) {
	defer func() { _ = recover() }()
	owner.report(problem)
}

func (owner *CommandRuntime) mintCommandID() ID {
	nextSequence := owner.sequence.Add(1)
	return ID(fmt.Sprintf("cmd-%s-%d", owner.instance, nextSequence))
}

func (owner *CommandRuntime) unregister(entry *registeredCommand) {
	owner.mutex.Lock()
	if owner.entries[entry.specification.Name] == entry {
		delete(owner.entries, entry.specification.Name)
	}
	entry.close()
	owner.mutex.Unlock()
}

// Close withdraws every remaining registration and drains admitted handlers.
func (owner *CommandRuntime) Close(closeContext context.Context) error {
	if closeContext == nil {
		return errors.New("commands: close Context is nil")
	}
	owner.mutex.Lock()
	entries := make([]*registeredCommand, 0, len(owner.entries))
	for nameValue, entry := range owner.entries {
		delete(owner.entries, nameValue)
		entry.close()
		entries = append(entries, entry)
	}
	owner.mutex.Unlock()
	var closeErr error
	for _, entry := range entries {
		closeErr = errors.Join(closeErr, entry.wait(closeContext))
	}
	return closeErr
}

func normalizeDefinition(specification Definition) (*registeredCommand, error) {
	if !commandNamePattern.MatchString(specification.Name) {
		return nil, fmt.Errorf(
			"commands: command name %q must match %s",
			specification.Name,
			commandNamePattern,
		)
	}
	if strings.TrimSpace(specification.Description) == "" {
		return nil, fmt.Errorf(
			"commands: command %q description must not be empty",
			specification.Name,
		)
	}
	if specification.Handler == nil {
		return nil, fmt.Errorf(
			"commands: command %q handler is nil",
			specification.Name,
		)
	}
	if specification.Input != nil && strings.TrimSpace(specification.Input.Hint) == "" {
		return nil, fmt.Errorf(
			"commands: command %q input hint must not be empty",
			specification.Name,
		)
	}
	if specification.Input != nil && specification.Input.Images {
		return nil, fmt.Errorf(
			"commands: command %q requires an image attachment admission capability that is not composed",
			specification.Name,
		)
	}
	idle := make(chan struct{})
	close(idle)
	normalized := cloneDefinition(specification)
	return &registeredCommand{
		specification: normalized,
		descriptor: Descriptor{
			Name:        normalized.Name,
			Description: normalized.Description,
			Input:       cloneInput(normalized.Input),
		},
		quiescent: idle,
	}, nil
}

func validateResult(outcome Result) error {
	switch outcome.Kind {
	case ResultSuccess:
		if outcome.SourceEventSeq != nil &&
			(*outcome.SourceEventSeq < 0 || *outcome.SourceEventSeq > maxSafeInteger) {
			return errors.New("commands: success sourceEventSeq must be a non-negative safe integer")
		}
		return nil
	case ResultError:
		if outcome.Text == nil || strings.TrimSpace(*outcome.Text) == "" {
			return errors.New("commands: error result text must not be empty")
		}
		if outcome.SourceEventSeq != nil {
			return errors.New("commands: error result cannot cite sourceEventSeq")
		}
		return nil
	default:
		return fmt.Errorf("commands: unknown result kind %q", outcome.Kind)
	}
}

func validateSourceEvent(conversation *session.Session, outcome Result) error {
	if outcome.SourceEventSeq == nil {
		return nil
	}
	for _, entry := range conversation.Events() {
		if entry.Seq != *outcome.SourceEventSeq {
			continue
		}
		if entry.Type == RunEventName || entry.Type == DoneEventName {
			return fmt.Errorf(
				"commands: sourceEventSeq %d identifies a command lifecycle event",
				*outcome.SourceEventSeq,
			)
		}
		return nil
	}
	return fmt.Errorf(
		"commands: sourceEventSeq %d does not identify a prior Session event",
		*outcome.SourceEventSeq,
	)
}

func (entry *registeredCommand) begin() bool {
	entry.mutex.Lock()
	defer entry.mutex.Unlock()
	if entry.closed {
		return false
	}
	if entry.active == 0 {
		entry.quiescent = make(chan struct{})
	}
	entry.active++
	return true
}

func (entry *registeredCommand) end() {
	entry.mutex.Lock()
	entry.active--
	if entry.active == 0 {
		close(entry.quiescent)
	}
	entry.mutex.Unlock()
}

func (entry *registeredCommand) close() {
	entry.mutex.Lock()
	entry.closed = true
	entry.mutex.Unlock()
}

func (entry *registeredCommand) wait(waitContext context.Context) error {
	entry.mutex.Lock()
	quiescent := entry.quiescent
	entry.mutex.Unlock()
	select {
	case <-quiescent:
		return nil
	case <-waitContext.Done():
		return waitContext.Err()
	}
}

func cloneDefinition(original Definition) Definition {
	return Definition{
		Name:        original.Name,
		Description: original.Description,
		Input:       cloneInput(original.Input),
		RecordInput: cloneBool(original.RecordInput),
		Handler:     original.Handler,
	}
}

func cloneDescriptor(original Descriptor) Descriptor {
	return Descriptor{
		Name:        original.Name,
		Description: original.Description,
		Input:       cloneInput(original.Input),
	}
}

func cloneInput(original *InputDescriptor) *InputDescriptor {
	if original == nil {
		return nil
	}
	detached := *original
	return &detached
}

func cloneResult(original Result) Result {
	return Result{
		Kind:           original.Kind,
		Text:           cloneString(original.Text),
		SourceEventSeq: cloneInt64(original.SourceEventSeq),
	}
}

func cloneString(original *string) *string {
	if original == nil {
		return nil
	}
	detached := *original
	return &detached
}

func cloneInt64(original *int64) *int64 {
	if original == nil {
		return nil
	}
	detached := *original
	return &detached
}

func cloneBool(original *bool) *bool {
	if original == nil {
		return nil
	}
	detached := *original
	return &detached
}
