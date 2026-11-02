package dbbridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"

	"academic/backend/internal/model"
)

func replyPartitionKey(course, class, topic string) string {
	b, _ := json.Marshal([]string{course, class, topic})
	return "discussion_reply_user:" + base64.RawURLEncoding.EncodeToString(b)
}
func (r *platformRepo) ReplyUsers(ctx context.Context, scope model.DWSScope, course, class, topic string) (out model.ReplyUserList, err error) {
	out.Items = []model.ReplyUser{}
	out.Coverage = "top_level_only"
	if course == "" || class == "" || topic == "" || len(course) > 256 || len(class) > 256 || len(topic) > 512 || strings.ContainsAny(course+class+topic, "\x00") {
		return out, model.ErrInvalidArgument
	}
	c, e := r.Get(ctx, scope)
	if e != nil {
		return out, e
	}
	if c.Source != "chaoxing" {
		return out, model.ErrNotFound
	}
	rows, e := r.db.pool.Query(ctx, `WITH latest AS(SELECT DISTINCT ON(external_id,parent_id)external_id,content FROM platform_records WHERE user_id=$1 AND connection_id=$2 AND kind='discussion_reply_user' AND parent_id=$3 AND content->'fields'->>'course_id'=$4 AND content->'fields'->>'class_id'=$5 ORDER BY external_id,parent_id,id DESC) SELECT COALESCE(content->'fields'->>'user_id',''),COALESCE(content->'fields'->>'user_name',''),COALESCE(content->'fields'->>'anonymous','false'),count(*) FROM latest GROUP BY 1,2,3,CASE WHEN COALESCE(content->'fields'->>'user_id','')='' THEN external_id ELSE '' END ORDER BY 2,1`, scope.UserID, scope.ConnectionID, topic, course, class)
	if e != nil {
		return out, storageError(e)
	}
	defer rows.Close()
	for rows.Next() {
		var u model.ReplyUser
		var anonymous string
		if e = rows.Scan(&u.UserID, &u.UserName, &anonymous, &u.ReplyCount); e != nil {
			return out, storageError(e)
		}
		u.Anonymous = anonymous == "true"
		out.ReplyObservationCount += u.ReplyCount
		out.Items = append(out.Items, u)
	}
	if e = rows.Err(); e != nil {
		return out, storageError(e)
	}
	e = r.db.pool.QueryRow(ctx, `SELECT max(updated_at) FROM platform_checkpoints WHERE user_id=$1 AND connection_id=$2 AND kind=$3`, scope.UserID, scope.ConnectionID, replyPartitionKey(course, class, topic)).Scan(&out.LastFetchedAt)
	return out, storageError(e)
}

var _ model.ReplyUserRepository = (*platformRepo)(nil)
