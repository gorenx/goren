package file

import (
	"encoding/json"

	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/v2"
)

// Merge combines named sparse inputs in increasing precedence. It never uses
// koanf's weak unmarshalling: the result passes the same typed decoder as files.
func Merge(destination any, sources ...any) error {
	merged := koanf.New(".")
	for _, input := range sources {
		encoded, err := json.Marshal(input)
		if err != nil {
			return ErrInvalid
		}
		var values map[string]any
		if json.Unmarshal(encoded, &values) != nil {
			return ErrInvalid
		}
		if err := merged.Load(confmap.Provider(values, ""), nil); err != nil {
			return ErrInvalid
		}
	}
	encoded, err := json.Marshal(merged.Raw())
	if err != nil {
		return ErrInvalid
	}
	return decodeTyped(encoded, destination)
}
