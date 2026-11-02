package model

import (
	"context"
	"time"
)

type ReplyUser struct {
	UserID     string `json:"user_id"`
	UserName   string `json:"user_name"`
	Anonymous  bool   `json:"anonymous"`
	ReplyCount int64  `json:"reply_count"`
}
type ReplyUserList struct {
	Items                 []ReplyUser `json:"items"`
	ReplyObservationCount int64       `json:"reply_observation_count"`
	Coverage              string      `json:"coverage"`
	LastFetchedAt         *time.Time  `json:"last_fetched_at,omitempty"`
}
type ReplyUserRepository interface {
	ReplyUsers(context.Context, DWSScope, string, string, string) (ReplyUserList, error)
}
