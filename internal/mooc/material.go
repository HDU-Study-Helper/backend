package mooc

import (
	"context"
	"net/http"

	"academic/backend/internal/contract"
)

func enrichUnit(ctx context.Context, h *http.Client, co course, row *contract.Record) error {
	typ := text(row.Fields["contentType"])
	content := text(row.Fields["contentId"])
	unit := text(row.Fields["unitId"])
	if row.ParentID != co.ref() || row.Fields["sourceTermId"] != sourceTerm(co) || row.Fields["source_collection"] != "units" {
		return contract.ErrInvalid
	}
	v, e := readUnitBody(ctx, h, co, content, typ, unit)
	if e != nil {
		return e
	}
	m := object(v)
	if m == nil {
		return contract.ErrParse
	}
	if id := text(m["unitId"]); id != "" && id != unit {
		return contract.ErrParse
	}
	f := row.Fields
	f["source_body_endpoint"] = "CourseBean.getLessonUnitLearnVo"
	f["body_status"] = "read"
	delete(f, "body_reason")
	switch typ {
	case "3":
		for _, k := range []string{"textPages", "textPageWhRatio"} {
			if scalar(m[k]) {
				f[k] = m[k]
			}
		}
		f["body_status"] = "metadata_only"
		f["body_reason"] = "PDF bytes not fetched; signed source links stay private"
	case "4":
		raw, ok := m["htmlContent"].(string)
		if !ok {
			return contract.ErrParse
		}
		f["body_empty_source"] = raw == ""
		body, e := plainBody(raw)
		if e != nil {
			return e
		}
		row.Content = body
	case "6":
		post := object(m["post"])
		if post == nil {
			return contract.ErrParse
		}
		raw, ok := post["content"].(string)
		if !ok || !validID(text(post["id"])) {
			return contract.ErrParse
		}
		f["body_empty_source"] = raw == ""
		body, e := plainBody(raw)
		if e != nil {
			return e
		}
		row.Content = body
		f["postId"] = text(post["id"])
		for _, k := range []string{"gmtCreate", "gmtModified", "postTime", "countReply", "countVote", "countBrowse", "lastReplyTime", "type", "deleted", "lockFlag", "tagLector", "tagTop", "tagSolve"} {
			if scalar(post[k]) {
				f[k] = post[k]
			}
		}
		if title := text(post["title"]); title != "" {
			f["post_title"] = title
		}
		f["discussion_coverage"] = "root post only; replies and participant identities not fetched"
	default:
		return contract.ErrUnsupported
	}
	return nil
}
