package dbbridge

import (
	"context"
	"encoding/json"
	"strings"

	"academic/backend/internal/model"
	"github.com/jackc/pgx/v5"
)

// The caller already partitions by refresh round, owner and source. Keep each
// connection/conversation separate before applying the existing batch limits.
func groupDWSControlEvents(ctx context.Context, tx pgx.Tx, events []controlSealEvent) ([]controlSealGroup, error) {
	groups := []controlSealGroup{}
	indexes := map[[3]string]int{}
	for _, event := range events {
		if err := json.Unmarshal(event.raw, &event.change); err != nil {
			return nil, err
		}
		group, err := controlDWSGroup(ctx, tx, event)
		if err != nil {
			return nil, err
		}
		scope := &model.ControlBatchScope{Kind: "group", ConnectionID: event.change.ConnectionID, GroupID: group}
		key := [3]string{scope.ConnectionID, group, ""}
		if scope.ConnectionID == "" || group == "" {
			// Missing or conflicting identity must not merge unrelated messages.
			// Preserve each event in a blocked batch for explicit review.
			scope.Kind = "unresolved_group"
			key[2] = event.id
		}
		index, exists := indexes[key]
		if !exists {
			index = len(groups)
			indexes[key] = index
			groups = append(groups, controlSealGroup{first: event, scope: scope})
		}
		groups[index].events = append(groups[index].events, event)
	}
	return groups, nil
}

func controlDWSGroup(ctx context.Context, tx pgx.Tx, event controlSealEvent) (string, error) {
	change := event.change
	if change.ConnectionID == "" {
		return "", nil
	}
	group := change.GroupID
	for _, raw := range []json.RawMessage{change.After, change.Before} {
		var snapshot struct {
			ConversationID string `json:"conversationId"`
		}
		if json.Unmarshal(raw, &snapshot) != nil || snapshot.ConversationID == "" {
			continue
		}
		if group != "" && group != snapshot.ConversationID {
			return "", nil
		}
		group = snapshot.ConversationID
	}
	if group != "" || change.Resource != "attachment" {
		return group, nil
	}
	// Legacy unsealed attachment events have a central message ID followed by
	// ':' and the attachment ID. Resolve only inside the exact owner/connection.
	messageID, attachmentID, found := strings.Cut(change.RecordID, ":")
	if !found || messageID == "" || attachmentID == "" {
		return "", nil
	}
	err := tx.QueryRow(ctx, `SELECT group_id FROM dws_messages WHERE user_id=$1 AND connection_id=$2 AND id::text=$3`, event.owner, change.ConnectionID, messageID).Scan(&group)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return group, err
}
