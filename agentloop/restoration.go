package agentloop

import (
	"encoding/json"
	"fmt"

	"github.com/gorenx/goren/session"
)

func restoreLastTurn(conversation *session.Session) (int64, error) {
	entries := conversation.Events()
	for index := len(entries) - 1; index >= 0; index-- {
		if entries[index].Type != session.TurnStartEventName {
			continue
		}
		var started session.TurnStart
		if err := json.Unmarshal(entries[index].Data, &started); err != nil ||
			started.Turn <= 0 || started.Turn > maxSafeInteger {
			return 0, fmt.Errorf(
				"agentloop: invalid persisted turn/start at seq %d",
				entries[index].Seq,
			)
		}
		return started.Turn, nil
	}
	return 0, nil
}
