package persistence

import (
	"context"
	"errors"
	"testing"

	"github.com/gorenx/goren/session"
)

type readSource struct {
	ReadStorage
	segment  *EventSegment
	window   *EventWindow
	artifact *RawArtifact
	failure  error
}

func (source *readSource) ReadEventsFrom(context.Context, session.SessionID, EventContinuation) (*EventSegment, error) {
	return source.segment, source.failure
}
func (source *readSource) ReadEventsBefore(context.Context, session.SessionID, EventPage) (*EventWindow, error) {
	return source.window, source.failure
}
func (*readSource) Locate(session.Header) (Location, bool) { return Location{}, false }
func (*readSource) SupportsRawArtifacts() bool             { return true }
func (source *readSource) ReadRaw(context.Context, session.SessionID) (*RawArtifact, error) {
	return source.artifact, source.failure
}

func TestReaderClassifiesInvalidPagesAndKeepsStorageErrors(t *testing.T) {
	ctx := context.Background()
	header := session.Header{
		ID:      "log",
		Version: session.FormatVersion,
	}
	for _, scenario := range []struct {
		name        string
		page        *EventSegment
		unsupported bool
		missing     bool
	}{
		{
			name:    "missing",
			missing: true,
		},
		{
			name: "wrong identity",
			page: &EventSegment{
				Header: session.Header{
					ID:      "other",
					Version: session.FormatVersion,
				},
			},
		},
		{
			name:        "unsupported format",
			unsupported: true,
			page: &EventSegment{
				Header: session.Header{
					ID:      "log",
					Version: session.FormatVersion + 1,
				},
			},
		},
		{
			name:        "unknown required event",
			unsupported: true,
			page: &EventSegment{
				Header: header,
				Events: []session.Event{
					{
						Seq:  0,
						Type: "unknown",
					},
				},
			},
		},
		{
			name: "gap",
			page: &EventSegment{
				Header: header,
				Events: []session.Event{
					{
						Seq:       1,
						Type:      "extension",
						Ignorable: true,
					},
				},
			},
		},
		{
			name: "empty continued page",
			page: &EventSegment{
				Header:  header,
				HasMore: true,
			},
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			source := &readSource{
				segment: scenario.page,
			}
			queries, err := NewReader(source)
			if err != nil {
				t.Fatal(err)
			}
			_, err = queries.ReadEventsFrom(ctx, "log", EventContinuation{
				Limit: 1,
			})
			var missing *NotFoundError
			var unsupported *UnsupportedFormatError
			var corrupt *CorruptionError
			switch {
			case scenario.missing:
				if !errors.As(err, &missing) {
					t.Fatalf("missing classification: %v", err)
				}
			case scenario.unsupported:
				if !errors.As(err, &unsupported) {
					t.Fatalf("format classification: %v", err)
				}
			default:
				if !errors.As(err, &corrupt) {
					t.Fatalf("corruption classification: %v", err)
				}
			}
		})
	}
	failure := errors.New("storage unavailable")
	queries, _ := NewReader(&readSource{
		failure: failure,
	})
	if _, err := queries.ReadEventsFrom(ctx, "log", EventContinuation{
		Limit: 1,
	}); !errors.Is(err, failure) {
		t.Fatalf("storage error changed: %v", err)
	}
}

func TestReaderDetachesPageAndRawMetadata(t *testing.T) {
	cwd := "original"
	source := &readSource{
		segment: &EventSegment{
			Header: session.Header{
				ID:      "log",
				Version: session.FormatVersion,
				CWD:     &cwd,
			},
			Events: []session.Event{
				{
					Seq:       0,
					Type:      "extension",
					Ignorable: true,
					Data:      []byte(`{}`),
				},
			},
		},
		artifact: &RawArtifact{
			Header: session.Header{
				CWD: &cwd,
			},
			Content: "raw text",
		},
	}
	queries, _ := NewReader(source)
	page, err := queries.ReadEventsFrom(context.Background(), "log", EventContinuation{
		Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	*page.Header.CWD = "changed"
	page.Events[0].Data[0] = '!'
	raw, err := queries.ReadRaw(context.Background(), "log")
	if err != nil {
		t.Fatal(err)
	}
	*raw.Header.CWD = "also changed"
	if cwd != "original" || string(source.segment.Events[0].Data) != `{}` {
		t.Fatal("read output aliases stored input")
	}
}
