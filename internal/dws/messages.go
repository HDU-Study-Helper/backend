package dws

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"academic/backend/internal/model"
)

// ResourceRef is upstream metadata, never a downloadable URL.
type ResourceRef struct {
	AttachmentID       string `json:"attachmentId,omitempty"`
	Type               string `json:"type"`
	ResourceID         string `json:"resourceId"`
	MessageID          string `json:"messageId"`
	OpenConversationID string `json:"openConversationId"`
	Name               string `json:"name,omitempty"`
	Downloadable       bool   `json:"downloadable"`
	Download           *struct {
		Arguments map[string]string `json:"arguments"`
		Ready     bool              `json:"ready"`
	} `json:"download,omitempty"`
}

// Observation preserves source content. Labels are heuristic, unconfirmed tags.
type Observation struct {
	MessageID      string          `json:"messageId"`
	ConversationID string          `json:"conversationId"`
	Sender         string          `json:"sender,omitempty"`
	SenderID       string          `json:"senderId,omitempty"`
	MessageType    string          `json:"messageType"`
	Text           *string         `json:"text"`
	CreateTime     json.RawMessage `json:"createTime"`
	UpdateTime     json.RawMessage `json:"updateTime,omitempty"`
	ReplyID        string          `json:"replyId,omitempty"`
	SenderType     string          `json:"senderType,omitempty"`
	ThreadID       string          `json:"threadId,omitempty"`
	QuotedMessage  json.RawMessage `json:"quotedMessage,omitempty"`
	Forwarded      json.RawMessage `json:"forwarded,omitempty"`
	ResourceRefs   []ResourceRef   `json:"resourceRefs"`
	Labels         []string        `json:"labels"`
	Verification   string          `json:"verification"`
}
type messagePage struct {
	ContractVersion    string            `json:"contractVersion"`
	Count              *int              `json:"count"`
	Messages           []Observation     `json:"messages"`
	Complete           *bool             `json:"complete"`
	HasMore            *bool             `json:"hasMore"`
	PaginationKnown    *bool             `json:"paginationKnown"`
	NextPageToken      string            `json:"nextPageToken"`
	FailedCount        int               `json:"failedCount"`
	Failures           []json.RawMessage `json:"failures"`
	Partial            bool              `json:"partial"`
	DecryptFailedCount int               `json:"decryptFailedCount"`
	Success            *bool             `json:"success"`
	Error              json.RawMessage   `json:"error"`
}

// sanitizeNested retains source statements and identity while excluding transport metadata.
func sanitizeNested(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	var clean func(any, int) any
	clean = func(v any, depth int) any {
		if depth > 16 {
			return nil
		}
		switch x := v.(type) {
		case map[string]any:
			out := map[string]any{}
			for k, v := range x {
				switch k {
				case "messageId", "conversationId", "sender", "senderId", "senderType", "messageType", "text", "createTime", "updateTime", "replyId", "threadId", "quotedMessage", "forwarded", "messages":
					out[k] = clean(v, depth+1)
				}
			}
			return out
		case []any:
			out := make([]any, 0, len(x))
			for _, v := range x {
				out = append(out, clean(v, depth+1))
			}
			return out
		default:
			return v
		}
	}
	b, _ := json.Marshal(clean(value, 0))
	return b
}

func parseTime(raw json.RawMessage) (time.Time, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return time.Time{}, fmt.Errorf("parse_failed")
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if t, e := time.Parse(time.RFC3339Nano, s); e == nil {
			return t, nil
		}
		loc, err := time.LoadLocation("Asia/Shanghai")
		if err != nil {
			loc = time.FixedZone("Asia/Shanghai", 8*3600)
		}
		if t, e := time.ParseInLocation("2006-01-02 15:04:05", s, loc); e == nil {
			return t.UTC(), nil
		}
		n, e := strconv.ParseInt(s, 10, 64)
		if e == nil {
			return time.UnixMilli(n).UTC(), nil
		}
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return time.UnixMilli(n).UTC(), nil
	}
	return time.Time{}, fmt.Errorf("parse_failed")
}
func labels(o Observation) []string {
	out := []string{}
	text := ""
	if o.Text != nil {
		text = *o.Text
	}
	for _, rule := range []struct {
		name  string
		words []string
	}{{"assignment", []string{"作业", "提交", "报告"}}, {"exam", []string{"考试", "测验", "期末"}}, {"notice", []string{"通知", "公告"}}} {
		for _, word := range rule.words {
			if strings.Contains(text, word) {
				out = append(out, rule.name)
				break
			}
		}
	}
	if len(o.ResourceRefs) > 0 {
		out = append(out, "file")
	}
	if len(out) == 0 {
		out = append(out, "ordinary")
	}
	return out
}
func parsePage(data []byte, group string, start, end time.Time) (messagePage, []model.Message, error) {
	var p messagePage
	if json.Unmarshal(data, &p) != nil || p.ContractVersion != "im.message-list.v1" || p.Count == nil || p.Complete == nil || p.HasMore == nil || p.PaginationKnown == nil || !*p.PaginationKnown || p.Messages == nil || *p.Count != len(p.Messages) || p.Partial || p.FailedCount != 0 || len(p.Failures) > 0 || p.DecryptFailedCount != 0 || (p.Success != nil && !*p.Success) || (len(p.Error) > 0 && string(p.Error) != "null") {
		return p, nil, fmt.Errorf("parse_failed")
	}
	if !*p.HasMore && !*p.Complete {
		return p, nil, fmt.Errorf("parse_failed")
	}
	out := make([]model.Message, 0, len(p.Messages))
	for _, o := range p.Messages {
		t, err := parseTime(o.CreateTime)
		if err != nil || o.MessageID == "" || o.ConversationID != group || t.Before(start) || !t.Before(end) {
			return p, nil, fmt.Errorf("parse_failed")
		}
		version := ""
		if len(o.UpdateTime) > 0 && string(o.UpdateTime) != "null" {
			u, e := parseTime(o.UpdateTime)
			if e != nil {
				return p, nil, e
			}
			version = u.UTC().Format(time.RFC3339Nano)
		}
		for i := range o.ResourceRefs {
			ref := &o.ResourceRefs[i]
			if ref.Download != nil {
				a := ref.Download.Arguments
				if a["type"] == ref.Type && a["resource-id"] == ref.ResourceID {
					ref.MessageID = a["message-id"]
					ref.OpenConversationID = a["open-conversation-id"]
					if ref.Type == "fileId" && ref.MessageID == "" && ref.OpenConversationID == "" {
						ref.MessageID = o.MessageID
						ref.OpenConversationID = group
					}
					ref.Downloadable = ref.Download.Ready
				}
			}
			ref.Downloadable = (ref.Downloadable || ref.Download == nil) && (ref.Type == "fileId" || ref.Type == "mediaId") && ref.ResourceID != "" && ref.MessageID == o.MessageID && ref.OpenConversationID == group
			ref.Download = nil
		}
		o.QuotedMessage = sanitizeNested(o.QuotedMessage)
		o.Forwarded = sanitizeNested(o.Forwarded)
		if o.ResourceRefs == nil {
			o.ResourceRefs = []ResourceRef{}
		}
		for i := range o.ResourceRefs {
			o.ResourceRefs[i].AttachmentID = resourceKey(o.ResourceRefs[i])
		}
		o.Labels = labels(o)
		o.Verification = "unconfirmed"
		b, err := json.Marshal(o)
		if err != nil || len(b) > model.MaxMessageBytes {
			return p, nil, fmt.Errorf("parse_failed")
		}
		h := sha256.Sum256(b)
		out = append(out, model.Message{GroupID: group, ExternalID: o.MessageID, SourceVersion: version, ContentHash: hex.EncodeToString(h[:]), Content: b, ObservedAt: time.Now().UTC()})
	}
	return p, out, nil
}
