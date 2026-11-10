package mooc

import (
	"context"
	"net/url"
	"sort"

	"academic/backend/internal/contract"
)

// InspectReadDetails verifies exactly the official announcement read and safe
// material/discussion unit types. It emits shape only; never body text or URLs.
func InspectReadDetails(ctx context.Context, deps contract.Dependencies, scope contract.Scope, session contract.Session, emit func(map[string]any)) error {
	ai, e := New(deps)
	if e != nil {
		return e
	}
	a := ai.(*adapter)
	_, h, e := a.restore(scope, session)
	if e != nil {
		return e
	}
	co := course{ID: "1486679162", Term: "1488617444", SourceTerm: "1488350464", Type: 2}
	v, e := readAnnouncements(ctx, h, co)
	if e != nil {
		emit(map[string]any{"phase": "dwr_notices", "error": e.Error()})
	} else {
		emit(map[string]any{"phase": "dwr_notices", "shape": safeShape(v)})
	}
	dto, e := rpc(ctx, h, "courseBean.getLastLearnedMocTermDto", co.referer(), url.Values{"termId": {sourceTerm(co)}})
	if e != nil {
		return e
	}
	seen := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		if arr, ok := v.([]any); ok {
			for _, x := range arr {
				walk(x)
			}
			return
		}
		m := object(v)
		if m == nil {
			return
		}
		typ := text(m["contentType"])
		if (typ == "3" || typ == "4" || typ == "6") && !seen[typ] && object(m["test"]) == nil && validID(text(m["contentId"])) && validID(text(m["id"])) {
			seen[typ] = true
			v, e := readUnitBody(ctx, h, co, text(m["contentId"]), typ, text(m["id"]))
			if e != nil {
				out := map[string]any{"phase": "safe_unit_read", "type": typ, "error": e.Error()}
				if shape, ok := e.(*dwrShapeError); ok {
					out["shape"] = shape.shape
				}
				emit(out)
			} else {
				shape := safeShape(v)
				obj := object(v)
				for _, k := range []string{"resourceVo", "post", "pdfVo", "textVo", "discussionVo"} {
					if x := obj[k]; x != nil {
						shape[k] = safeShape(x)
						if k == "resourceVo" {
							if p := object(x)["post"]; p != nil {
								shape["resourceVo.post"] = safeShape(p)
							}
						}
					}
				}
				emit(map[string]any{"phase": "safe_unit_read", "type": typ, "shape": shape})
			}
		}
		for _, k := range []string{"mocTermDto", "chapters", "lessons", "units"} {
			if x := m[k]; x != nil {
				walk(x)
			}
		}
	}
	walk(dto)
	return nil
}
func safeShape(v any) map[string]any {
	out := map[string]any{}
	if arr, ok := v.([]any); ok {
		out["count"] = len(arr)
		if len(arr) > 0 {
			out["first"] = safeShape(arr[0])
		}
		return out
	}
	m := object(v)
	ks := []string{}
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	out["keys"] = ks
	return out
}
