package dbbridge

import "academic/backend/internal/model"

func checkpointKind(l model.PlatformLease, kind string) string {
	if l.CheckpointKey != "" {
		return l.CheckpointKey
	}
	return kind
}
