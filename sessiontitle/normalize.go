package sessiontitle

import (
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	oscSequence        = regexp.MustCompile(`(?:\x1B\]|\x{009D})(?s:.*?)(?:\x07|\x1B\\|$)`)
	csiSequence        = regexp.MustCompile(`(?:\x1B\[|\x{009B})[0-?]*[ -/]*[@-~]`)
	escSequence        = regexp.MustCompile(`\x1B[@-_]`)
	controlCharacter   = regexp.MustCompile(`[\x00-\x08\x0B\x0C\x0E-\x1F\x7F-\x{009F}]`)
	directionalControl = regexp.MustCompile(`[\x{200B}\x{200E}\x{200F}\x{202A}-\x{202E}\x{2060}-\x{2064}\x{2066}-\x{206F}\x{FEFF}]`)
)

// TruncateTitleUTF8 returns the longest rune prefix within a positive byte budget.
func TruncateTitleUTF8(input string, maxBytes int) (string, error) {
	if maxBytes <= 0 {
		return "", errors.New("sessiontitle: maxBytes must be a positive integer")
	}
	input = strings.ToValidUTF8(input, "")
	if len(input) <= maxBytes {
		return input, nil
	}
	used := 0
	for index, character := range input {
		width := utf8.RuneLen(character)
		if used+width > maxBytes {
			return input[:index], nil
		}
		used += width
	}
	return input, nil
}

// NormalizeSessionTitle removes deceptive terminal/control content, folds
// whitespace to one line, and applies a UTF-8-safe byte cap.
func NormalizeSessionTitle(input string, maxBytes int) (string, error) {
	cleaned := cleanTitleText(input)
	bounded, err := TruncateTitleUTF8(cleaned, maxBytes)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(bounded, " \t\r\n"), nil
}

// FallbackSessionTitle derives leading normalized words under both caps.
func FallbackSessionTitle(input string, maxWords int, maxBytes int) (string, error) {
	if maxWords <= 0 {
		return "", errors.New("sessiontitle: maxWords must be a positive integer")
	}
	words := strings.Fields(cleanTitleText(input))
	if len(words) > maxWords {
		words = words[:maxWords]
	}
	bounded, err := TruncateTitleUTF8(strings.Join(words, " "), maxBytes)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(bounded, " \t\r\n"), nil
}

func cleanTitleText(input string) string {
	cleaned := strings.ToValidUTF8(input, "")
	cleaned = oscSequence.ReplaceAllString(cleaned, "")
	cleaned = csiSequence.ReplaceAllString(cleaned, "")
	cleaned = escSequence.ReplaceAllString(cleaned, "")
	cleaned = controlCharacter.ReplaceAllString(cleaned, "")
	cleaned = directionalControl.ReplaceAllString(cleaned, "")
	return strings.Join(strings.Fields(cleaned), " ")
}
