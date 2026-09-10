package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gorenx/goren/userquestions"
	"github.com/mark3labs/mcp-go/mcp"
)

type questionFixture struct {
	ask func(context.Context, userquestions.Request) (userquestions.Answer, error)
}

func (fixture *questionFixture) Ask(ctx context.Context, request userquestions.Request) (userquestions.Answer, error) {
	return fixture.ask(ctx, request)
}

func consentAnswer(label string) userquestions.Answer {
	return userquestions.Answer{
		Answers: []userquestions.AnswerItem{
			{
				ID:       "mcp-consent",
				Selected: []string{label},
			},
		},
	}
}

func formRequest(schema string) mcp.ElicitationRequest {
	return mcp.ElicitationRequest{
		Params: mcp.ElicitationParams{
			Mode:            "form",
			Message:         "Provide values",
			RequestedSchema: json.RawMessage(schema),
		},
	}
}

func TestElicitorConsentAndCallerAdmission(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		answer  string
		failure error
		want    mcp.ElicitationResponseAction
	}{
		{
			name:   "decline",
			answer: "Decline",
			want:   "decline",
		},
		{
			name:   "cancel",
			answer: "Cancel",
			want:   "cancel",
		},
		{
			name: "missing-provider",
			failure: &userquestions.Error{
				Code: userquestions.CodeNoProvider,
			},
			want: "decline",
		},
		{
			name: "delegated",
			failure: &userquestions.Error{
				Code: userquestions.CodeDelegatedCaller,
			},
			want: "decline",
		},
		{
			name: "stale-agent",
			failure: &userquestions.Error{
				Code: userquestions.CodeCallerNotLive,
			},
			want: "decline",
		},
		{
			name: "dismiss",
			failure: &userquestions.Error{
				Code: userquestions.CodeCancelled,
			},
			want: "cancel",
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			calls := 0
			bridge := &elicitor{
				serverName: "fixture",
				questions: &questionFixture{
					ask: func(_ context.Context, input userquestions.Request) (userquestions.Answer, error) {
						calls++
						if input.SessionID != "exact-agent" {
							t.Fatal("lost Agent identity")
						}
						return consentAnswer(scenario.answer), scenario.failure
					},
				},
			}
			request := formRequest(`{"type":"object","properties":{"name":{"type":"string"}}}`)
			result, err := bridge.Elicit(t.Context(), request)
			if err != nil ||
				result.Action != "decline" ||
				calls != 0 {
				t.Fatal("unbound elicitation reached provider")
			}
			ctx := context.WithValue(t.Context(), callerKey{}, caller{
				sessionID: "exact-agent",
				callID:    "call",
			})
			result, err = bridge.Elicit(ctx, request)
			if err != nil ||
				result.Action != scenario.want ||
				calls != 1 {
				t.Fatalf("result=%+v error=%v calls=%d", result, err, calls)
			}
		})
	}
}

func TestElicitorReasksInvalidFormAndCancelsWaiting(t *testing.T) {
	for _, waiting := range []bool{false, true} {
		t.Run(map[bool]string{false: "invalid-then-valid", true: "cancel-wait"}[waiting], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), callerKey{}, caller{
				sessionID: "exact-agent",
				callID:    "call",
			}), 50*time.Millisecond)
			defer cancel()
			calls := 0
			bridge := &elicitor{
				questions: &questionFixture{
					ask: func(ctx context.Context, request userquestions.Request) (userquestions.Answer, error) {
						calls++
						if request.Questions[0].ID == "mcp-consent" {
							return consentAnswer("Continue"), nil
						}
						if waiting {
							<-ctx.Done()
							return userquestions.Answer{}, ctx.Err()
						}
						textValue := "wrong"
						if calls == 3 {
							textValue = "8"
						}
						return userquestions.Answer{
							Answers: []userquestions.AnswerItem{
								{
									ID:     "count",
									Custom: &textValue,
								},
							},
						}, nil
					},
				},
			}
			result, err := bridge.Elicit(ctx, formRequest(`{"type":"object","properties":{"count":{"type":"integer","minimum":2}},"required":["count"]}`))
			if err != nil {
				t.Fatal(err)
			}
			if waiting {
				if result.Action != "cancel" || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
					t.Fatal("waiting interaction not cancelled")
				}
			} else if result.Action != "accept" ||
				result.Content.(map[string]any)["count"] != json.Number("8") ||
				calls != 3 {
				t.Fatalf("invalid answer was not corrected: %+v", result)
			}
		})
	}
}

func TestElicitorRejectsUnsafeFormsAndURLsBeforeAsking(t *testing.T) {
	bridge := &elicitor{
		questions: &questionFixture{
			ask: func(context.Context, userquestions.Request) (userquestions.Answer, error) {
				t.Fatal("unsafe request shown to user")
				return userquestions.Answer{}, nil
			},
		},
	}
	ctx := context.WithValue(t.Context(), callerKey{}, caller{
		sessionID: "agent",
		callID:    "call",
	})
	for _, schema := range []string{
		`{"type":"object","properties":{"password":{"type":"string"}}}`,
		`{"type":"object","properties":{"nested":{"type":"object"}}}`,
		`{"type":"object","properties":{"link":{"$ref":"https://example.com/schema"}}}`,
		`{"type":"object","additionalProperties":{"$ref":"https://example.com/schema"}}`,
	} {
		result, err := bridge.Elicit(ctx, formRequest(schema))
		if err != nil || result.Action != "decline" {
			t.Fatalf("unsafe form: %+v %v", result, err)
		}
	}
	for _, address := range []string{"javascript:alert(1)", "http://example.com/path", "https://user:password@example.com/", "file:///etc/passwd"} {
		result, err := bridge.Elicit(ctx, mcp.ElicitationRequest{
			Params: mcp.ElicitationParams{
				Mode:          "url",
				URL:           address,
				ElicitationID: "link",
			},
		})
		if err != nil || result.Action != "decline" {
			t.Fatalf("unsafe URL: %+v %v", result, err)
		}
	}
}

func TestFormPreservesPrimitiveTypesAndValidatesConstraints(t *testing.T) {
	prepared, err := newForm(json.RawMessage(`{"type":"object","properties":{"enabled":{"type":"boolean"},"ratio":{"type":"number","minimum":0},"labels":{"type":"array","items":{"type":"string","enum":["a","b"]},"uniqueItems":true},"note":{"type":"string"}},"required":["enabled","ratio","labels"]}`))
	if err != nil {
		t.Fatal(err)
	}
	ratio := "0.75"
	input := userquestions.Answer{
		Answers: []userquestions.AnswerItem{
			{
				ID:       "enabled",
				Selected: []string{"true"},
			},
			{
				ID:     "ratio",
				Custom: &ratio,
			},
			{
				ID:       "labels",
				Selected: []string{"a", "b"},
			},
		},
	}
	content, err := prepared.answer(input)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(content)
	if strings.Contains(string(encoded), "note") ||
		content["enabled"] != true ||
		content["ratio"] != json.Number("0.75") {
		t.Fatalf("wrong primitive mapping: %s", encoded)
	}
	input.Answers[2].Selected = []string{"a", "a"}
	if _, err := prepared.answer(input); err == nil {
		t.Fatal("duplicate array answer accepted")
	}
}

func TestFormPreservesEmptyValuesAndLargeIntegers(t *testing.T) {
	prepared, err := newForm(json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"},"count":{"type":"integer"},"labels":{"type":"array","items":{"type":"string","enum":["a"]}}},"required":["name","count","labels"]}`))
	if err != nil {
		t.Fatal(err)
	}
	empty := ""
	large := "9007199254740993"
	content, err := prepared.answer(userquestions.Answer{
		Answers: []userquestions.AnswerItem{
			{
				ID:     "name",
				Custom: &empty,
			},
			{
				ID:     "count",
				Custom: &large,
			},
			{
				ID:       "labels",
				Selected: []string{},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(content)
	if err != nil || string(encoded) != `{"count":9007199254740993,"labels":[],"name":""}` {
		t.Fatalf("elicitation values changed: %s %v", encoded, err)
	}
}
