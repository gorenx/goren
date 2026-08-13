package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// TokenCount records a token count and whether it is an estimate.
type TokenCount struct {
	InputTokens int
	Estimated   bool
	Strategy    string
}

// TokenCounter counts one prepared Context for a target model.
type TokenCounter interface {
	CountTokens(context.Context, Model, Context) (TokenCount, error)
}

// TokenCounterFunc adapts a function to TokenCounter.
type TokenCounterFunc func(context.Context, Model, Context) (TokenCount, error)

type toolArgumentsValidator interface {
	Validate(any) error
}

type compiledToolSchema struct {
	parameters string
	validator  toolArgumentsValidator
}

// CountTokens calls the adapted token-counting function.
func (countStrategy TokenCounterFunc) CountTokens(ctx context.Context, targetModel Model, input Context) (TokenCount, error) {
	return countStrategy(ctx, targetModel, input)
}

// ConservativeTokenCounter is a deterministic upper-bound fallback. It counts
// every serialized UTF-8 byte as a token and records that strategy explicitly.
type ConservativeTokenCounter struct{}

// CountTokens returns a conservative estimated token count.
func (ConservativeTokenCounter) CountTokens(_ context.Context, _ Model, input Context) (TokenCount, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return TokenCount{}, fmt.Errorf("estimate context tokens: %w", err)
	}
	return TokenCount{
		InputTokens: len(encoded),
		Estimated:   true,
		Strategy:    "serialized-utf8-byte-upper-bound-v1",
	}, nil
}

// ResolveStreamOptions freezes invocation-owned data, validates it against the
// target model, and resolves reasoning effort and its configured token budget.
func ResolveStreamOptions(targetModel Model, invocationOptions StreamOptions) (StreamOptions, error) {
	resolvedOptions := invocationOptions.Clone()
	if err := ValidateOptions(resolvedOptions); err != nil {
		return StreamOptions{}, err
	}
	if err := ValidateModelOptions(targetModel, resolvedOptions); err != nil {
		return StreamOptions{}, err
	}
	resolvedLevel, _, configuredBudget, err := ResolveReasoning(targetModel, resolvedOptions.Reasoning)
	if err != nil {
		return StreamOptions{}, err
	}
	resolvedOptions.Reasoning = resolvedLevel
	if resolvedOptions.MaxOutputTokens == 0 {
		resolvedOptions.MaxOutputTokens = targetModel.MaxOutputTokens
	}
	if resolvedOptions.ThinkingBudget == 0 {
		resolvedOptions.ThinkingBudget = configuredBudget
	}
	return resolvedOptions, nil
}

// ValidateToolCall validates a complete call against the matching function
// tool's JSON Schema without coercing argument values.
func ValidateToolCall(toolDefinitions []Tool, requestedCall ToolCall) error {
	var definition *Tool
	for index := range toolDefinitions {
		if toolDefinitions[index].Name == requestedCall.Name {
			definition = &toolDefinitions[index]
			break
		}
	}
	if definition == nil {
		return fmt.Errorf("tool %q is not defined", requestedCall.Name)
	}
	if len(requestedCall.Arguments) == 0 || !json.Valid(requestedCall.Arguments) {
		return fmt.Errorf("tool %q arguments are invalid JSON", requestedCall.Name)
	}

	compiledSchema := definition.validator
	if compiledSchema == nil || definition.validated != string(definition.Parameters) {
		var err error
		compiledSchema, err = compileToolSchema(*definition)
		if err != nil {
			return err
		}
	}
	argumentValue, err := jsonschema.UnmarshalJSON(bytes.NewReader(requestedCall.Arguments))
	if err != nil {
		return fmt.Errorf("tool %q arguments: %w", requestedCall.Name, err)
	}
	if err := compiledSchema.Validate(argumentValue); err != nil {
		var validationFailure *jsonschema.ValidationError
		if errors.As(err, &validationFailure) {
			validationFailure = deepestValidationFailure(validationFailure)
			path := "/" + strings.Join(validationFailure.InstanceLocation, "/")
			if path == "/" {
				path = "$"
			}
			return fmt.Errorf("tool %q arguments at %s: %w", requestedCall.Name, path, err)
		}
		return fmt.Errorf("tool %q arguments: %w", requestedCall.Name, err)
	}
	return nil
}

func compileToolSchema(definition Tool) (*jsonschema.Schema, error) {
	schemaDocument, err := jsonschema.UnmarshalJSON(bytes.NewReader(definition.Parameters))
	if err != nil {
		return nil, fmt.Errorf("tool %q schema: %w", definition.Name, err)
	}
	compiler := jsonschema.NewCompiler()
	const schemaLocation = "mem://goren/tool-schema.json"
	if err := compiler.AddResource(schemaLocation, schemaDocument); err != nil {
		return nil, fmt.Errorf("tool %q schema: %w", definition.Name, err)
	}
	compiledSchema, err := compiler.Compile(schemaLocation)
	if err != nil {
		return nil, fmt.Errorf("tool %q schema: %w", definition.Name, err)
	}
	return compiledSchema, nil
}

func deepestValidationFailure(validationFailure *jsonschema.ValidationError) *jsonschema.ValidationError {
	deepest := validationFailure
	for _, cause := range validationFailure.Causes {
		candidate := deepestValidationFailure(cause)
		if len(candidate.InstanceLocation) >= len(deepest.InstanceLocation) {
			deepest = candidate
		}
	}
	return deepest
}

// ResolveReasoning clamps a requested effort to targetModel capabilities and
// returns the provider-facing mapped value and optional token budget.
func ResolveReasoning(targetModel Model, requested ReasoningLevel) (ReasoningLevel, string, int, error) {
	if requested == "" {
		return "", "", 0, nil
	}
	if !knownReasoningLevel(requested) {
		return "", "", 0, fmt.Errorf("unsupported reasoning level %q", requested)
	}
	if requested == ReasoningOff {
		mapped := targetModel.ReasoningMap[ReasoningOff]
		if mapped == "" {
			mapped = "none"
		}
		return ReasoningOff, mapped, 0, nil
	}
	if !targetModel.Reasoning {
		return "", "", 0, errors.New("target model does not support reasoning")
	}
	resolved := requested
	if len(targetModel.ReasoningLevels) > 0 {
		resolved = clampReasoning(targetModel.ReasoningLevels, requested)
	}
	mapped := string(resolved)
	if configured := targetModel.ReasoningMap[resolved]; configured != "" {
		mapped = configured
	}
	return resolved, mapped, targetModel.ReasoningBudget[resolved], nil
}

func clampReasoning(supported []ReasoningLevel, requested ReasoningLevel) ReasoningLevel {
	rank := map[ReasoningLevel]int{
		ReasoningOff: 0, ReasoningMinimal: 1, ReasoningLow: 2, ReasoningMedium: 3,
		ReasoningHigh: 4, ReasoningXHigh: 5, ReasoningMax: 6,
	}
	requestedRank := rank[requested]
	var lower ReasoningLevel
	lowerRank := -1
	var upper ReasoningLevel
	upperRank := int(^uint(0) >> 1)
	for _, supportedLevel := range supported {
		supportedRank := rank[supportedLevel]
		if supportedRank == requestedRank {
			return supportedLevel
		}
		if supportedRank < requestedRank && supportedRank > lowerRank {
			lower = supportedLevel
			lowerRank = supportedRank
		}
		if supportedRank > requestedRank && supportedRank < upperRank {
			upper = supportedLevel
			upperRank = supportedRank
		}
	}
	if upper != "" {
		return upper
	}
	if lower != "" {
		return lower
	}
	return supported[0]
}
