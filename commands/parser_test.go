package commands

import (
	"reflect"
	"testing"
)

func TestParseCommandPreservesExactTrailingInput(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		line       string
		want       ParsedLine
		wantParsed bool
	}{
		{
			line: "/compact",
			want: ParsedLine{
				Name: "compact",
			},
			wantParsed: true,
		},
		{
			line: "/compact  now ",
			want: ParsedLine{
				Name:     "compact",
				RawInput: "  now ",
			},
			wantParsed: true,
		},
		{
			line: "/a_b-2\nnext",
			want: ParsedLine{
				Name:     "a_b-2",
				RawInput: "\nnext",
			},
			wantParsed: true,
		},
		{
			line: "compact",
		},
		{
			line: "/Compact",
		},
		{
			line: "/compact/value",
		},
		{
			line: " /compact",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.line, func(t *testing.T) {
			parsedValue, matched := ParseCommand(testCase.line)
			if matched != testCase.wantParsed || !reflect.DeepEqual(parsedValue, testCase.want) {
				t.Fatalf(
					"ParseCommand(%q) = (%#v, %t), want (%#v, %t)",
					testCase.line,
					parsedValue,
					matched,
					testCase.want,
					testCase.wantParsed,
				)
			}
		})
	}
}
