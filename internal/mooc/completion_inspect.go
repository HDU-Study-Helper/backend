package mooc

import (
	"context"
	"net/url"
	"strconv"

	"academic/backend/internal/contract"
)

// InspectDiscussionSync exercises the production Sync entry with bounded calls,
// reporting only endpoint paths and cursor counts; it does not commit records.
func InspectDiscussionSync(ctx context.Context, deps contract.Dependencies, scope contract.Scope, session contract.Session, emit func(map[string]any)) error {
	a, e := New(deps)
	if e != nil {
		return e
	}
	raw := ""
	for i := 0; i < 12; i++ {
		p, e := a.Sync(ctx, scope, session, "discussion", raw)
		if e != nil {
			return e
		}
		counts := map[string]int{}
		for _, r := range p.Records {
			counts[text(r.Fields["discussion_record_type"])]++
		}
		out := map[string]any{"phase": "production_sync", "step": i, "records": len(p.Records), "record_types": counts, "has_more": p.HasMore}
		if p.HasMore {
			c, e := newCursor(scope, "discussion", p.NextCursor)
			if e != nil {
				return e
			}
			out["pending"] = len(c.Pending)
			out["discussion_active"] = c.Discussion != nil
			out["courses"] = len(c.Courses)
			if len(c.Pending) > 0 {
				out["first_pending_needs_body"] = needsUnitBody(c.Pending[0])
				out["first_pending_contentType"] = c.Pending[0].Fields["contentType"]
				out["first_pending_collection"] = c.Pending[0].Fields["source_collection"]
			}
		}
		emit(out)
		if counts["reply"] > 0 || !p.HasMore {
			return nil
		}
		raw = p.NextCursor
		session = p.Session
	}
	return nil
}

// InspectCompletion emits only whitelisted scalars and response key names. It
// reuses the caller's in-memory session and never persists source data.
func InspectCompletion(ctx context.Context, deps contract.Dependencies, scope contract.Scope, session contract.Session, emit func(map[string]any)) error {
	ai, e := New(deps)
	if e != nil {
		return e
	}
	_, h, e := ai.(*adapter).restore(scope, session)
	if e != nil {
		return e
	}
	for typ := 1; typ <= 2; typ++ {
		courses, _, _, e := coursePage(ctx, h, typ, 1)
		if e != nil {
			return e
		}
		for _, co := range courses {
			co, e = discoverCourse(ctx, h, co)
			if e != nil {
				return e
			}
			emit(map[string]any{"phase": "identity_coverage", "available": co.CurrentUser != ""})
			if typ == 2 {
				co, e = discoverCourse(ctx, h, co)
				if e != nil {
					return e
				}
				if co.ActiveTerm != "" {
					co.SourceTerm = co.ActiveTerm
				}
			}
			dto, e := rpc(ctx, h, "courseBean.getLastLearnedMocTermDto", co.referer(), url.Values{"termId": {sourceTerm(co)}})
			if e != nil {
				return e
			}
			emit(map[string]any{"phase": "catalog_completion", "routeTermId": co.Term, "sourceTermId": sourceTerm(co), "shape": inspectShape(dto)})
			for _, kind := range []contract.Resource{"chapter", "lesson", contract.Assignment, contract.Exam, contract.Progress} {
				rows, e := termRecords(dto, co, kind)
				if e != nil {
					return e
				}
				if len(rows) > 0 {
					emit(map[string]any{"phase": "catalog_sample", "resource": kind, "count": len(rows), "fields": rows[0].Fields})
				}
			}
			rows, e := termRecords(dto, co, "discussion")
			if e != nil {
				return e
			}
			for i, row := range rows {
				if i >= 1 {
					break
				}
				if e = enrichUnit(ctx, h, co, &row); e != nil {
					return e
				}
				post := text(row.Fields["postId"])
				for _, method := range []string{"getPostDetailById", "getPaginationReplys"} {
					params := []string{post}
					if method == "getPaginationReplys" {
						params = append(params, "1", strconv.Itoa(1))
					}
					v, e := readPostDWR(ctx, h, co, method, params)
					if e != nil {
						emit(map[string]any{"phase": "discussion_completion", "method": method, "error": e.Error()})
						continue
					}
					shape := safeShape(v)
					m := object(v)
					shape["pagination_values"] = metadataPagination(m)
					shape["current_user_state"] = userState(m)
					for k, x := range m {
						if object(x) != nil {
							shape[k] = safeShape(x)
						}
						if ar, ok := x.([]any); ok {
							shape[k] = safeShape(ar)
							if len(ar) > 0 {
								for ck, cv := range object(ar[0]) {
									if object(cv) != nil {
										shape[k+".first."+ck] = safeShape(cv)
									}
								}
							}
						}
					}
					for _, k := range []string{"query", "pagination"} {
						if x := object(m[k]); x != nil {
							shape[k+"_values"] = metadataPagination(x)
						}
					}
					emit(map[string]any{"phase": "discussion_completion", "method": method, "shape": shape})
				}
				c := cursor{Discussion: &discussionCursor{Root: row, Page: 1, Seen: map[string]bool{}}}
				if _, e := advanceDiscussion(ctx, h, co, &c); e != nil {
					return e
				}
				replies, e := advanceDiscussion(ctx, h, co, &c)
				if e != nil {
					return e
				}
				own := 0
				for _, r := range replies {
					if r.Fields["is_current_user"] == true {
						own++
					}
				}
				emit(map[string]any{"phase": "implemented_reply_parse", "records": len(replies), "current_user_replies": own, "has_more": c.Discussion != nil, "fields": func() any {
					if len(replies) > 0 {
						return safeShape(replies[0].Fields)
					}
					return nil
				}()})
			}
			// One complete safe source sample per invocation keeps diagnostics bounded.
			return nil
		}
	}
	return nil
}
func metadataPagination(m map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range []string{"pageIndex", "pageNo", "pageSize", "totlePageCount", "totleCount", "totalPageCount", "totalCount"} {
		if scalar(m[k]) {
			out[k] = m[k]
		}
	}
	return out
}
