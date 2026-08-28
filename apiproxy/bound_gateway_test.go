package apiproxy_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gorenx/goren/apiproxy"
	"github.com/gorenx/goren/connection"
	boundcontract "github.com/gorenx/goren/subagent/bound"
)

type boundDefinitionsStub struct {
	definitions []boundcontract.Definition
	createErr   error
	replaceErr  error
	creation    boundcontract.Creation
	replacement boundcontract.Replacement
}

func (stub *boundDefinitionsStub) List(
	context.Context,
) ([]boundcontract.Definition, error) {
	return append([]boundcontract.Definition(nil), stub.definitions...), nil
}

func (stub *boundDefinitionsStub) Create(
	_ context.Context,
	creation boundcontract.Creation,
) (boundcontract.Definition, error) {
	stub.creation = creation
	if stub.createErr != nil {
		return boundcontract.Definition{}, stub.createErr
	}
	return boundcontract.NewDefinition(creation.Definition, 1)
}

func (stub *boundDefinitionsStub) Replace(
	_ context.Context,
	replacement boundcontract.Replacement,
) (boundcontract.Definition, error) {
	stub.replacement = replacement
	if stub.replaceErr != nil {
		return boundcontract.Definition{}, stub.replaceErr
	}
	return boundcontract.NewDefinition(
		replacement.Definition,
		replacement.ExpectedRevision+1,
	)
}

func TestBoundGatewayRegistersStrictDefinitionCommands(
	testingContext *testing.T,
) {
	definitionValue := boundGatewayDefinition(testingContext)
	capability := &boundDefinitionsStub{
		definitions: []boundcontract.Definition{definitionValue},
	}
	methods := apiproxy.NewCatalog()
	if err := apiproxy.RegisterBoundAPI(
		methods,
		apiproxy.NewBoundGateway(capability),
	); err != nil {
		testingContext.Fatal(err)
	}
	for _, method := range []string{
		apiproxy.BoundListMethod,
		apiproxy.BoundCreateMethod,
		apiproxy.BoundReplaceMethod,
	} {
		if !methods.HasUnary(method) {
			testingContext.Fatalf("Bound method %q is not registered", method)
		}
	}
	listed, err := methods.DispatchUnary(
		context.Background(),
		apiproxy.BoundListMethod,
		"bound-list",
		json.RawMessage(`{}`),
	)
	if err != nil || !listed.OK {
		testingContext.Fatalf("bound.list = (%#v, %v)", listed, err)
	}
	var listValue apiproxy.BoundListValue
	if err = json.Unmarshal(listed.Value, &listValue); err != nil {
		testingContext.Fatal(err)
	}
	if len(listValue.Definitions) != 1 ||
		listValue.Definitions[0].Name != definitionValue.Name {
		testingContext.Fatalf("bound.list value = %#v", listValue)
	}
	created, err := methods.DispatchUnary(
		context.Background(),
		apiproxy.BoundCreateMethod,
		"bound-create",
		json.RawMessage(`{"definition":{"name":"writer","enabled":true,"systemPrompt":"write","extensions":[]}}`),
	)
	if err != nil || !created.OK || capability.creation.Definition.Name != "writer" {
		testingContext.Fatalf("bound.create = (%#v, %v)", created, err)
	}
	replaced, err := methods.DispatchUnary(
		context.Background(),
		apiproxy.BoundReplaceMethod,
		"bound-replace",
		json.RawMessage(`{"expectedRevision":1,"definition":{"name":"writer","enabled":false,"systemPrompt":"write again","extensions":[]}}`),
	)
	if err != nil || !replaced.OK ||
		capability.replacement.ExpectedRevision != 1 {
		testingContext.Fatalf("bound.replace = (%#v, %v)", replaced, err)
	}
	invalid, err := methods.DispatchUnary(
		context.Background(),
		apiproxy.BoundCreateMethod,
		"bound-invalid",
		json.RawMessage(`{"definition":{"name":"writer","enabled":true,"systemPrompt":"write","extensions":[],"unknown":true}}`),
	)
	if err != nil || invalid.OK || invalid.Error == nil ||
		invalid.Error.Code != connection.ErrorBadRequest {
		testingContext.Fatalf("invalid bound.create = (%#v, %v)", invalid, err)
	}
}

func TestBoundGatewayMapsOwnedDefinitionFailures(
	testingContext *testing.T,
) {
	testCases := []struct {
		caseName     string
		businessCode boundcontract.ErrorCode
		wireCode     connection.RPCErrorCode
		method       string
	}{
		{
			caseName:     "exists",
			businessCode: boundcontract.ErrorDefinitionExists,
			wireCode:     connection.ErrorBoundDefinitionExists,
			method:       apiproxy.BoundCreateMethod,
		},
		{
			caseName:     "not found",
			businessCode: boundcontract.ErrorDefinitionNotFound,
			wireCode:     connection.ErrorBoundDefinitionNotFound,
			method:       apiproxy.BoundReplaceMethod,
		},
		{
			caseName:     "conflict",
			businessCode: boundcontract.ErrorDefinitionConflict,
			wireCode:     connection.ErrorBoundDefinitionConflict,
			method:       apiproxy.BoundReplaceMethod,
		},
		{
			caseName:     "rejected",
			businessCode: boundcontract.ErrorDefinitionRejected,
			wireCode:     connection.ErrorBoundDefinitionRejected,
			method:       apiproxy.BoundCreateMethod,
		},
	}
	for _, testCase := range testCases {
		testingContext.Run(testCase.caseName, func(testingContext *testing.T) {
			businessErr := &boundcontract.Error{
				Code:    testCase.businessCode,
				Message: "definition rejected",
			}
			capability := &boundDefinitionsStub{}
			payload := json.RawMessage(`{"definition":{"name":"researcher","enabled":true,"systemPrompt":"prompt","extensions":[]}}`)
			if testCase.method == apiproxy.BoundReplaceMethod {
				capability.replaceErr = businessErr
				payload = json.RawMessage(`{"expectedRevision":2,"definition":{"name":"researcher","enabled":true,"systemPrompt":"prompt","extensions":[]}}`)
			} else {
				capability.createErr = businessErr
			}
			methods := apiproxy.NewCatalog()
			if err := apiproxy.RegisterBoundAPI(
				methods,
				apiproxy.NewBoundGateway(capability),
			); err != nil {
				testingContext.Fatal(err)
			}
			result, err := methods.DispatchUnary(
				context.Background(),
				testCase.method,
				"bound-failure",
				payload,
			)
			if err != nil || result.OK || result.Error == nil ||
				result.Error.Code != testCase.wireCode {
				testingContext.Fatalf("result = (%#v, %v)", result, err)
			}
		})
	}
}

func TestBoundGatewayPropagatesUnownedFailure(
	testingContext *testing.T,
) {
	sentinel := errors.New("definition store unavailable")
	methods := apiproxy.NewCatalog()
	if err := apiproxy.RegisterBoundAPI(
		methods,
		apiproxy.NewBoundGateway(&boundDefinitionsStub{
			createErr: sentinel,
		}),
	); err != nil {
		testingContext.Fatal(err)
	}
	_, err := methods.DispatchUnary(
		context.Background(),
		apiproxy.BoundCreateMethod,
		"bound-technical-failure",
		json.RawMessage(`{"definition":{"name":"researcher","enabled":true,"systemPrompt":"prompt","extensions":[]}}`),
	)
	if !errors.Is(err, sentinel) {
		testingContext.Fatalf("technical error = %v", err)
	}
}

func boundGatewayDefinition(
	testingContext *testing.T,
) boundcontract.Definition {
	testingContext.Helper()
	definitionValue, err := boundcontract.NewDefinition(
		boundcontract.Draft{
			Name:         "researcher",
			Enabled:      true,
			SystemPrompt: "research",
		},
		1,
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	return definitionValue
}
