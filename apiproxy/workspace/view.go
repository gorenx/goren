package workspaceapi

import (
	"errors"

	sessionframes "github.com/gorenx/goren/apiproxy/session/events"
)

// ID identifies one workspace registry entry.
type ID string

// View is the browser-visible projection of a workspace record.
type View struct {
	WorkspaceID ID                        `json:"workspaceId"`
	Path        string                    `json:"path"`
	Title       string                    `json:"title"`
	SessionIDs  []sessionframes.SessionID `json:"sessionIds"`
	CreatedAt   string                    `json:"createdAt"`
	UpdatedAt   string                    `json:"updatedAt"`
}

func validateWorkspace(snapshot View) error {
	if snapshot.WorkspaceID == "" {
		return errors.New("workspaceId must be non-empty")
	}
	if snapshot.SessionIDs == nil {
		return errors.New("workspace sessionIds must be an array")
	}
	for _, identifier := range snapshot.SessionIDs {
		if err := sessionframes.ValidateSessionID(identifier); err != nil {
			return err
		}
	}
	return nil
}
