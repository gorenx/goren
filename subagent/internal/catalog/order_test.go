package catalog

import (
	"reflect"
	"testing"

	"github.com/gorenx/goren/session"
)

func TestSiblingOrderMatchesSourceLocaleAndInsertionStability(t *testing.T) {
	identifiers := []session.SessionID{
		"_",
		"-",
		"a",
		"A",
		"ä",
		"B",
		"child-10",
		"child-2",
		"e\u0301",
		"é",
		"z",
	}
	records := make([]sessionRecord, 0, len(identifiers))
	for ordinal := len(identifiers) - 1; ordinal >= 0; ordinal-- {
		records = append(records, sessionRecord{
			header: session.Header{
				ID:        identifiers[ordinal],
				CreatedAt: 1,
			},
			ordinal: ordinal,
		})
	}
	newSiblingOrder().sort(records)
	ordered := make([]session.SessionID, len(records))
	for index, record := range records {
		ordered[index] = record.header.ID
	}
	want := []session.SessionID{
		"_",
		"-",
		"a",
		"A",
		"ä",
		"B",
		"child-10",
		"child-2",
		"e\u0301",
		"é",
		"z",
	}
	if !reflect.DeepEqual(ordered, want) {
		t.Fatalf("ordered ids = %#v, want %#v", ordered, want)
	}
}

func TestSiblingOrderUsesCreatedAtBeforeIdentifier(t *testing.T) {
	records := []sessionRecord{
		{
			header: session.Header{
				ID:        "a",
				CreatedAt: 2,
			},
			ordinal: 0,
		},
		{
			header: session.Header{
				ID:        "z",
				CreatedAt: 1,
			},
			ordinal: 1,
		},
	}
	newSiblingOrder().sort(records)
	if records[0].header.ID != "z" {
		t.Fatalf("first id = %q, want z", records[0].header.ID)
	}
}
