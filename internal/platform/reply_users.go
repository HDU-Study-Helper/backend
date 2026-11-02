package platform

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"academic/backend/internal/contract"
	"academic/backend/internal/model"
)

func replyPartitionKey(course, class, topic string) string {
	b, _ := json.Marshal([]string{course, class, topic})
	return "discussion_reply_user:" + base64.RawURLEncoding.EncodeToString(b)
}
func (s *Service) FetchReplyUsers(ctx context.Context, scope model.DWSScope, course, class, topic string) (model.ReplyUserList, error) {
	if course == "" || class == "" || topic == "" || len(course) > 256 || len(class) > 256 || len(topic) > 512 || strings.ContainsAny(course+class+topic, "\x00") {
		return model.ReplyUserList{}, contract.ErrInvalid
	}
	e := s.operate(ctx, scope, "", func(ctx context.Context, l model.PlatformLease, state model.PlatformState, a contract.Adapter) error {
		if state.Connection.Source != "chaoxing" || state.Connection.State != "active" {
			return contract.ErrAuthRequired
		}
		reader, ok := a.(contract.ReplyUserReader)
		if !ok {
			return contract.ErrUnsupported
		}
		raw, e := s.vault.Open(scope.UserID, scope.ConnectionID, state.Session)
		if e != nil {
			return e
		}
		defer clear(raw)
		page, e := reader.DiscussionReplyUsers(ctx, adapterScope(scope), raw, course, class, topic)
		if e != nil {
			return e
		}
		if page.HasMore || len(page.Records) > 500 {
			return contract.ErrParse
		}
		records := []model.PlatformRecord{}
		for _, record := range page.Records {
			if record.Kind != contract.DiscussionReplyUser || record.ExternalID == "" || record.ParentID != topic {
				return contract.ErrParse
			}
			record.Content = ""
			safe := map[string]any{}
			for _, key := range []string{"course_id", "class_id", "topic_id", "user_id", "user_name", "anonymous", "coverage"} {
				if v, ok := record.Fields[key]; ok {
					safe[key] = v
				}
			}
			record.Fields = safe
			if safe["course_id"] != course || safe["class_id"] != class || safe["topic_id"] != topic {
				return contract.ErrParse
			}
			observed := record.ObservedAt
			if observed.IsZero() {
				observed = time.Now().UTC()
			}
			record.ObservedAt = time.Time{}
			content, e := json.Marshal(record)
			if e != nil {
				return e
			}
			h := sha256.Sum256(content)
			records = append(records, model.PlatformRecord{Source: "chaoxing", Kind: string(record.Kind), ExternalID: record.ExternalID, ParentID: topic, Hash: hex.EncodeToString(h[:]), Content: content, ObservedAt: observed})
		}
		updated := page.Session
		if updated == nil {
			updated = raw
		}
		sealed, e := s.vault.Seal(scope.UserID, scope.ConnectionID, updated)
		if e != nil {
			return e
		}
		cursor, e := s.vault.Seal(scope.UserID, scope.ConnectionID, nil)
		if e != nil {
			return e
		}
		l.CheckpointKey = replyPartitionKey(course, class, topic)
		return s.repo.Commit(ctx, l, string(contract.DiscussionReplyUser), records, sealed, cursor)
	})
	if e != nil {
		return model.ReplyUserList{}, e
	}
	return s.ReplyUsers(ctx, scope, course, class, topic)
}
func (s *Service) ReplyUsers(ctx context.Context, scope model.DWSScope, course, class, topic string) (model.ReplyUserList, error) {
	r, ok := s.repo.(model.ReplyUserRepository)
	if !ok {
		return model.ReplyUserList{}, contract.ErrUnsupported
	}
	return r.ReplyUsers(ctx, scope, course, class, topic)
}
