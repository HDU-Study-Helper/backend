package mooc

import (
	"academic/backend/internal/contract"
	"context"
	"net/http"
	"net/url"
	"time"
)

// Separate official course statistics; never replace catalog lesson counts or
// derive completePercent from a denominator whose source meaning is different.
func readLearningRate(ctx context.Context, h *http.Client, co course) ([]contract.Record, error) {
	result, e := rpc(ctx, h, "mocCourseV2RpcBean.getMemberLearningRate", co.referer(), url.Values{"termId": {sourceTerm(co)}})
	if e != nil {
		return nil, e
	}
	term := nativeID(result, "termId")
	if term != "" && term != sourceTerm(co) {
		return nil, contract.ErrParse
	}
	counters := map[string]any{}
	partial := false
	for _, field := range []string{"lessonLearned", "lessonsCount"} {
		raw, present := result[field]
		if !present {
			return nil, contract.ErrParse
		}
		if raw == nil {
			counters[field] = nil
			partial = true
			continue
		}
		value, ok := nonnegativeSourceInteger(raw)
		if !ok {
			return nil, contract.ErrParse
		}
		counters[field] = value
	}
	f := sourceFields(co)
	f["source_endpoint"] = "mocCourseV2RpcBean.getMemberLearningRate"
	f["catalog_node_type"] = "term_statistics"
	f["lessonLearned"] = counters["lessonLearned"]
	f["lessonsCount"] = counters["lessonsCount"]
	f["native_progress_fields"] = counters
	f["progress_status"] = "source_fields_observed"
	if partial {
		f["progress_status"] = "partial_source_fields_observed"
	}
	f["progress_coverage"] = "official_member_learning_rate; independent_from_catalog_node_counts"
	f["progress_value_semantics"] = "source lesson counters only; no derived percentage or course completion"
	f["body_status"] = "not_applicable"
	return []contract.Record{{Kind: contract.Progress, ExternalID: key(co.ID, co.Term, sourceTerm(co), "member_learning_rate"), ParentID: co.ref(), Title: co.Name + " · 原站学习统计", Fields: f, ObservedAt: time.Now().UTC()}}, nil
}
