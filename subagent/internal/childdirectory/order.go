package childdirectory

import (
	"sort"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

// siblingOrder is the stable ordering relation used for one parent's
// children. American English makes the source runtime's default en-US
// localeCompare behavior deterministic in the Go server.
type siblingOrder struct {
	identifiers *collate.Collator
}

func newSiblingOrder() *siblingOrder {
	return &siblingOrder{
		identifiers: collate.New(language.AmericanEnglish),
	}
}

func (ordering *siblingOrder) sort(records []sessionRecord) {
	sort.SliceStable(records, func(leftIndex int, rightIndex int) bool {
		left := records[leftIndex]
		right := records[rightIndex]
		if left.header.CreatedAt != right.header.CreatedAt {
			return left.header.CreatedAt < right.header.CreatedAt
		}
		comparison := ordering.identifiers.CompareString(
			string(left.header.ID),
			string(right.header.ID),
		)
		if comparison != 0 {
			return comparison < 0
		}
		return left.ordinal < right.ordinal
	})
}
