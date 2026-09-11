package commands

import "regexp"

var commandNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// ParsedLine preserves a command name and its exact trailing input.
type ParsedLine struct {
	Name     string
	RawInput string
}

// ParseCommand parses slash syntax without normalizing input.
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
