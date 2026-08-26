package sessionapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	api "github.com/gorenx/goren/apiproxy"
	"github.com/gorenx/goren/session"
	sesspersist "github.com/gorenx/goren/session/persistence"
)

type listCursor struct {
	CreatedAt int64             `json:"createdAt"`
	SessionID session.SessionID `json:"sessionId"`
}

const maximumSessionCursorTime int64 = 1<<53 - 1

func decodeSessionListPage(request api.SessionListRequest) (sesspersist.SessionPage, error) {
	limit := defaultSessionListPageSize
	if request.Limit != nil {
		limit = *request.Limit
	}
	if limit < 1 || limit > maximumSessionListPageSize {
		return sesspersist.SessionPage{}, fmt.Errorf(
			"session.list: limit must be between 1 and %d",
			maximumSessionListPageSize,
		)
	}
	page := sesspersist.SessionPage{
		Limit: limit,
	}
	if request.Cursor == nil {
		return page, nil
	}
	rawValue, err := base64.RawURLEncoding.DecodeString(*request.Cursor)
	if err != nil {
		return sesspersist.SessionPage{}, errors.New("session.list: cursor is not valid base64url")
	}
	decoder := json.NewDecoder(bytes.NewReader(rawValue))
	decoder.DisallowUnknownFields()
	var decoded listCursor
	if err := decoder.Decode(&decoded); err != nil {
		return sesspersist.SessionPage{}, errors.New("session.list: cursor is invalid")
	}
	if err := ensureCursorEOF(decoder); err != nil {
		return sesspersist.SessionPage{}, err
	}
	if decoded.CreatedAt < 0 || decoded.CreatedAt > maximumSessionCursorTime || decoded.SessionID == "" {
		return sesspersist.SessionPage{}, errors.New("session.list: cursor is invalid")
	}
	page.Cursor = &sesspersist.SessionCursor{
		CreatedAt: decoded.CreatedAt,
		ID:        decoded.SessionID,
	}
	return page, nil
}

func encodeSessionListCursor(cursor *sesspersist.SessionCursor) (*string, error) {
	if cursor == nil {
		return nil, nil
	}
	rawValue, err := json.Marshal(listCursor{
		CreatedAt: cursor.CreatedAt,
		SessionID: cursor.ID,
	})
	if err != nil {
		return nil, fmt.Errorf("session.list: encode cursor: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(rawValue)
	return &encoded, nil
}

func ensureCursorEOF(decoder *json.Decoder) error {
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("session.list: cursor is invalid")
	}
	return nil
}
