package mooc

import (
	"academic/backend/internal/contract"
	"time"
)

func noticeRecords(v any, co course) ([]contract.Record, error) {
	rows, ok := v.([]any)
	if !ok || len(rows) > 2000 {
		return nil, contract.ErrParse
	}
	out := []contract.Record{}
	seen := map[string]bool{}
	for _, v := range rows {
		m := object(v)
		id := text(m["id"])
		if !validID(id) || seen[id] {
			return nil, contract.ErrParse
		}
		seen[id] = true
		if tid := text(m["termId"]); tid != "" && tid != sourceTerm(co) {
			return nil, contract.ErrParse
		}
		f := sourceFields(co)
		mergeFields(f, metadata(m, nil))
		for _, k := range []string{"publishTime", "status", "weight", "type"} {
			if scalar(m[k]) {
				f[k] = m[k]
			}
		}
		f["source_endpoint"] = "CourseBean.getAllAnnouncementByTerm"
		f["source_collection"] = "announcements"
		f["noticeId"] = id
		f["body_status"] = "read"
		f["coverage_reason"] = "term announcements only; teacher/global notices not requested"
		raw, ok := m["content"].(string)
		if !ok {
			return nil, contract.ErrParse
		}
		f["body_empty_source"] = raw == ""
		body, e := plainBody(raw)
		if e != nil {
			return nil, e
		}
		out = append(out, contract.Record{Kind: contract.Notice, ExternalID: key(co.ID, co.Term, sourceTerm(co), "notice", id), ParentID: co.ref(), Title: text(m["title"]), Content: body, Fields: f, ObservedAt: time.Now().UTC()})
	}
	return out, nil
}
