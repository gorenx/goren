// Package jsonvalue owns lossless JSON-value validation and detachment shared
// by same-process business boundaries.
package jsonvalue

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Clone validates one complete lossless JSON value and returns detached bytes.
func Clone(rawValue json.RawMessage) (json.RawMessage, error) {
	if err := Validate(rawValue); err != nil {
		return nil, err
	}
	return append(json.RawMessage(nil), rawValue...), nil
}

// Validate rejects duplicate object names, negative zero, non-finite or
// unrepresentable numbers, malformed input, and trailing JSON values.
func Validate(rawValue json.RawMessage) error {
	if !json.Valid(rawValue) {
		var decoded json.RawMessage
		return json.Unmarshal(rawValue, &decoded)
	}
	validator := valueValidator{
		rawValue: rawValue,
	}
	validator.skipWhitespace()
	if err := validator.scanValue(); err != nil {
		return err
	}
	validator.skipWhitespace()
	if validator.offset != len(rawValue) {
		return fmt.Errorf("multiple JSON values at byte %d", validator.offset)
	}
	return nil
}

// IsObject reports whether rawValue is one complete lossless JSON object.
func IsObject(rawValue json.RawMessage) bool {
	if err := Validate(rawValue); err != nil {
		return false
	}
	trimmed := bytes.TrimSpace(rawValue)
	return len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}'
}

type objectKey struct {
	start int
	end   int
}

type pathSegment struct {
	keyStart int
	keyEnd   int
	index    int
	array    bool
}

type valueValidator struct {
	rawValue   []byte
	offset     int
	keyCount   int
	pathDepth  int
	extraKeys  []objectKey
	extraPath  []pathSegment
	keyBuffer  [32]objectKey
	pathBuffer [16]pathSegment
}

func (validator *valueValidator) scanValue() error {
	validator.skipWhitespace()
	switch validator.rawValue[validator.offset] {
	case '{':
		return validator.scanObject()
	case '[':
		return validator.scanArray()
	case '"':
		validator.offset = scanStringEnd(validator.rawValue, validator.offset)
		return nil
	case 't':
		validator.offset += len("true")
		return nil
	case 'f':
		validator.offset += len("false")
		return nil
	case 'n':
		validator.offset += len("null")
		return nil
	default:
		return validator.scanNumber()
	}
}

func (validator *valueValidator) scanObject() error {
	validator.offset++
	validator.skipWhitespace()
	keyBase := validator.keyCount
	if validator.rawValue[validator.offset] == '}' {
		validator.offset++
		return nil
	}
	for {
		keyStart := validator.offset
		keyEnd := scanStringEnd(validator.rawValue, keyStart)
		candidate := objectKey{
			start: keyStart,
			end:   keyEnd,
		}
		for keyIndex := keyBase; keyIndex < validator.keyCount; keyIndex++ {
			existing := validator.keyAt(keyIndex)
			if validator.sameKey(existing, candidate) {
				return fmt.Errorf(
					"duplicate field %q at %s",
					validator.keyText(candidate),
					validator.pathText(),
				)
			}
		}
		validator.appendKey(candidate)
		validator.offset = keyEnd
		validator.skipWhitespace()
		validator.offset++
		validator.pushPath(pathSegment{
			keyStart: keyStart,
			keyEnd:   keyEnd,
		})
		if err := validator.scanValue(); err != nil {
			return err
		}
		validator.popPath()
		validator.skipWhitespace()
		if validator.rawValue[validator.offset] == '}' {
			validator.offset++
			validator.truncateKeys(keyBase)
			return nil
		}
		validator.offset++
		validator.skipWhitespace()
	}
}

func (validator *valueValidator) scanArray() error {
	validator.offset++
	validator.skipWhitespace()
	if validator.rawValue[validator.offset] == ']' {
		validator.offset++
		return nil
	}
	for index := 0; ; index++ {
		validator.pushPath(pathSegment{
			index: index,
			array: true,
		})
		if err := validator.scanValue(); err != nil {
			return err
		}
		validator.popPath()
		validator.skipWhitespace()
		if validator.rawValue[validator.offset] == ']' {
			validator.offset++
			return nil
		}
		validator.offset++
		validator.skipWhitespace()
	}
}

func (validator *valueValidator) scanNumber() error {
	start := validator.offset
	for validator.offset < len(validator.rawValue) &&
		!isValueDelimiter(validator.rawValue[validator.offset]) {
		validator.offset++
	}
	rawNumber := validator.rawValue[start:validator.offset]
	if accepted, decided := commonInteger(rawNumber); decided {
		if accepted {
			return nil
		}
		return fmt.Errorf(
			"invalid JSON number %q at %s",
			rawNumber,
			validator.pathText(),
		)
	}
	numeric := string(rawNumber)
	parsed, err := strconv.ParseFloat(numeric, 64)
	if err != nil || math.IsInf(parsed, 0) || math.IsNaN(parsed) ||
		parsed == 0 && math.Signbit(parsed) {
		return fmt.Errorf(
			"invalid JSON number %q at %s",
			numeric,
			validator.pathText(),
		)
	}
	return nil
}

// commonInteger handles the overwhelmingly common event-counter shape
// without converting bytes to a heap-backed string. Longer integers and every
// fractional/exponent form retain ParseFloat's representability check.
func commonInteger(rawNumber []byte) (bool, bool) {
	digits := rawNumber
	negative := len(digits) != 0 && digits[0] == '-'
	if negative {
		digits = digits[1:]
	}
	if len(digits) == 0 || len(digits) > 15 {
		return false, false
	}
	for _, digit := range digits {
		if digit < '0' || digit > '9' {
			return false, false
		}
	}
	if negative && len(digits) == 1 && digits[0] == '0' {
		return false, true
	}
	return true, true
}

func (validator *valueValidator) sameKey(left objectKey, right objectKey) bool {
	leftRaw := validator.rawValue[left.start:left.end]
	rightRaw := validator.rawValue[right.start:right.end]
	if bytes.IndexByte(leftRaw, '\\') < 0 && bytes.IndexByte(rightRaw, '\\') < 0 {
		return bytes.Equal(leftRaw, rightRaw)
	}
	return validator.keyText(left) == validator.keyText(right)
}

func (validator *valueValidator) keyText(selected objectKey) string {
	rawKey := validator.rawValue[selected.start:selected.end]
	if bytes.IndexByte(rawKey, '\\') < 0 {
		return string(rawKey[1 : len(rawKey)-1])
	}
	var decoded string
	_ = json.Unmarshal(rawKey, &decoded)
	return decoded
}

func (validator *valueValidator) pathText() string {
	var pathBuilder strings.Builder
	pathBuilder.WriteByte('$')
	for pathIndex := 0; pathIndex < validator.pathDepth; pathIndex++ {
		segment := validator.pathAt(pathIndex)
		if segment.array {
			pathBuilder.WriteByte('[')
			pathBuilder.WriteString(strconv.Itoa(segment.index))
			pathBuilder.WriteByte(']')
			continue
		}
		pathBuilder.WriteByte('.')
		pathBuilder.WriteString(validator.keyText(objectKey{
			start: segment.keyStart,
			end:   segment.keyEnd,
		}))
	}
	return pathBuilder.String()
}

func (validator *valueValidator) appendKey(selected objectKey) {
	if validator.extraKeys == nil && validator.keyCount < len(validator.keyBuffer) {
		validator.keyBuffer[validator.keyCount] = selected
		validator.keyCount++
		return
	}
	if validator.extraKeys == nil {
		validator.extraKeys = make([]objectKey, validator.keyCount, validator.keyCount*2)
		copy(validator.extraKeys, validator.keyBuffer[:validator.keyCount])
	}
	validator.extraKeys = append(validator.extraKeys, selected)
	validator.keyCount++
}

func (validator *valueValidator) keyAt(index int) objectKey {
	if validator.extraKeys != nil {
		return validator.extraKeys[index]
	}
	return validator.keyBuffer[index]
}

func (validator *valueValidator) truncateKeys(length int) {
	validator.keyCount = length
	if validator.extraKeys != nil {
		validator.extraKeys = validator.extraKeys[:length]
	}
}

func (validator *valueValidator) pushPath(segment pathSegment) {
	if validator.extraPath == nil && validator.pathDepth < len(validator.pathBuffer) {
		validator.pathBuffer[validator.pathDepth] = segment
		validator.pathDepth++
		return
	}
	if validator.extraPath == nil {
		validator.extraPath = make([]pathSegment, validator.pathDepth, validator.pathDepth*2)
		copy(validator.extraPath, validator.pathBuffer[:validator.pathDepth])
	}
	validator.extraPath = append(validator.extraPath, segment)
	validator.pathDepth++
}

func (validator *valueValidator) popPath() {
	validator.pathDepth--
	if validator.extraPath != nil {
		validator.extraPath = validator.extraPath[:validator.pathDepth]
	}
}

func (validator *valueValidator) pathAt(index int) pathSegment {
	if validator.extraPath != nil {
		return validator.extraPath[index]
	}
	return validator.pathBuffer[index]
}

func (validator *valueValidator) skipWhitespace() {
	for validator.offset < len(validator.rawValue) {
		switch validator.rawValue[validator.offset] {
		case ' ', '\t', '\n', '\r':
			validator.offset++
		default:
			return
		}
	}
}

func scanStringEnd(rawValue []byte, offset int) int {
	for offset++; ; offset++ {
		switch rawValue[offset] {
		case '\\':
			offset++
		case '"':
			return offset + 1
		}
	}
}

func isValueDelimiter(candidate byte) bool {
	switch candidate {
	case ' ', '\t', '\n', '\r', ',', ']', '}':
		return true
	default:
		return false
	}
}
