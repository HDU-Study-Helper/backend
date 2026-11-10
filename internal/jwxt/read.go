package jwxt

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"academic/backend/internal/contract"
)

type termCursor struct {
	Version  int
	Scope    contract.Scope
	Resource contract.Resource
	Year     int
	Term     int
	Index    int
	Records  []contract.Record
}

func (a *adapter) Sync(ctx context.Context, scope contract.Scope, session contract.Session, r contract.Resource, cursor string) (contract.Page, error) {
	if r != contract.Course && r != contract.Schedule {
		return contract.Page{}, contract.ErrUnsupported
	}
	return contract.Page{}, contract.ErrInvalid // require an explicit semester
}
func scalar(v json.RawMessage) string {
	var s string
	if json.Unmarshal(v, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(v, &n) == nil {
		return n.String()
	}
	return ""
}
func first(row map[string]json.RawMessage, keys ...string) string {
	for _, k := range keys {
		if s := scalar(row[k]); s != "" {
			return s
		}
	}
	return ""
}
func key(parts ...string) string { b, _ := json.Marshal(parts); return string(b) }
func observationHash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (a *adapter) SyncTerm(ctx context.Context, scope contract.Scope, session contract.Session, r contract.Resource, year, term int, raw string) (contract.Page, error) {
	if err := ctx.Err(); err != nil {
		return contract.Page{}, err
	}
	if r != contract.Course && r != contract.Schedule {
		return contract.Page{}, contract.ErrUnsupported
	}
	if year < 2000 || year > 2100 || (term != 1 && term != 2) {
		return contract.Page{}, contract.ErrInvalid
	}
	if len(session) == 0 {
		return contract.Page{}, contract.ErrAuthRequired
	}
	c, j, s, e := a.clientFor(scope, session)
	if e != nil {
		return contract.Page{}, e
	}
	menuURL, _ := url.Parse(menu)
	authorized := false
	for _, cookie := range j.Cookies(menuURL) {
		if cookie.Name == "JSESSIONID" && cookie.Value != "" {
			authorized = true
		}
	}
	if !authorized {
		return contract.Page{}, contract.ErrAuthRequired
	}
	v := termCursor{Version: 1, Scope: scope, Resource: r, Year: year, Term: term}
	if raw != "" {
		if len(raw) > 1<<20 {
			return contract.Page{}, contract.ErrInvalid
		}
		b, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || json.Unmarshal(b, &v) != nil || v.Version != 1 || v.Scope != scope || v.Resource != r || v.Year != year || v.Term != term || v.Index < 0 || v.Index >= len(v.Records) || len(v.Records) > 5000 {
			return contract.Page{}, contract.ErrInvalid
		}
	} else {
		xqm := "3"
		if term == 2 {
			xqm = "12"
		}
		form := url.Values{"xnm": {strconv.Itoa(year)}, "xqm": {xqm}}
		target := base + "kbcx/xskbcx_cxXsKb.html?gnmkdm=N2151"
		if r == contract.Course {
			target = base + "xsxk/zzxkyzb_cxZzxkYzbChoosedDisplay.html?gnmkdm=N253512"
			form = url.Values{"xkxnm": {strconv.Itoa(year)}, "xkxqm": {xqm}}
		}
		b, _, err := request(ctx, c, http.MethodPost, target, form, false)
		if err != nil {
			return contract.Page{}, err
		}
		if loginPage(b) {
			return contract.Page{}, contract.ErrAuthRequired
		}
		v.Records, e = parseRecords(b, r, year, term)
		if e != nil {
			return contract.Page{}, e
		}
	}
	end := v.Index + 100
	if end > len(v.Records) {
		end = len(v.Records)
	}
	rows := append([]contract.Record{}, v.Records[v.Index:end]...)
	now := time.Now().UTC()
	for i := range rows {
		rows[i].ObservedAt = now
	}
	out := contract.Page{Records: rows, HasMore: end < len(v.Records), Session: saveSession(s, j)}
	if out.HasMore {
		v.Index = end
		b, _ := json.Marshal(v)
		out.NextCursor = base64.RawURLEncoding.EncodeToString(b)
		if len(out.NextCursor) > 1<<20 {
			return contract.Page{}, contract.ErrParse
		}
	}
	return out, nil
}

func parseRecords(b []byte, r contract.Resource, year, term int) ([]contract.Record, error) {
	type entry map[string]json.RawMessage
	var rows, practice []entry
	if r == contract.Schedule {
		var payload struct {
			KB       json.RawMessage `json:"kbList"`
			Practice json.RawMessage `json:"sjkList"`
		}
		if json.Unmarshal(b, &payload) != nil || len(payload.KB) == 0 || json.Unmarshal(payload.KB, &rows) != nil || rows == nil {
			return nil, contract.ErrParse
		}
		if len(payload.Practice) > 0 && (json.Unmarshal(payload.Practice, &practice) != nil || practice == nil) {
			return nil, contract.ErrParse
		}
	} else {
		// This researched endpoint returns a complete JSON array, not jqGrid
		// items; reject an unfamiliar wrapper rather than silently losing pages.
		if json.Unmarshal(b, &rows) != nil || rows == nil {
			return nil, contract.ErrParse
		}
	}
	if len(rows)+len(practice) > 5000 {
		return nil, contract.ErrParse
	}
	out := []contract.Record{}
	seen := map[string]bool{}
	parent := key(strconv.Itoa(year), strconv.Itoa(term))
	for _, row := range rows {
		name := first(row, "kcmc")
		if name == "" {
			return nil, contract.ErrParse
		}
		courseID := first(row, "kch_id", "kch")
		classID := first(row, "jxb_id", "do_jxb_id")
		fields := map[string]any{"academic_year": year, "term": term, "entry_type": "course"}
		if courseID != "" {
			fields["course_id"] = courseID
		}
		if classID != "" {
			fields["class_id"] = classID
		}
		for source, dest := range map[string]string{"jxbmc": "class_name", "xf": "credits_raw", "kclbmc": "category", "kklxmc": "course_category", "kcxzmc": "nature", "xm": "teacher", "jsxx": "teacher_raw", "jsxm": "teacher_name", "cdmc": "location", "jxdd": "location_raw", "xqmc": "campus", "khfsmc": "assessment_raw", "zxs": "total_hours_raw", "zhxs": "weekly_hours_raw", "sksj": "schedule_raw"} {
			if value := scalar(row[source]); value != "" {
				fields[dest] = value
			}
		}
		var id string
		if r == contract.Schedule {
			day, err := strconv.Atoi(first(row, "xqj"))
			periods, weeks := first(row, "jc"), first(row, "zcd")
			if err != nil || day < 1 || day > 7 || periods == "" || weeks == "" {
				return nil, contract.ErrParse
			}
			fields["weekday"] = day
			fields["periods_raw"] = periods
			fields["weeks_raw"] = weeks
			// The API has no verified stable arrangement ID. Mark the fallback
			// explicitly; changed scheduling can create another observation.
			fields["entry_type"] = "class_session"
			fields["identity_source"] = "schedule_text_hash"
			id = "schedule:" + observationHash([]string{courseID, classID, name, strconv.Itoa(day), periods, weeks, first(row, "cdmc"), first(row, "xm")})
		} else {
			if courseID == "" || classID == "" {
				return nil, contract.ErrParse
			}
			id = key(courseID, classID)
		}
		if seen[id] {
			return nil, contract.ErrParse
		}
		seen[id] = true
		out = append(out, contract.Record{Kind: r, ExternalID: id, ParentID: parent, Title: name, Fields: fields})
	}
	for _, row := range practice {
		description := strings.TrimSpace(first(row, "qtkcgs"))
		if description == "" {
			return nil, contract.ErrParse
		}
		id := "practice:" + observationHash(description)
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, contract.Record{Kind: r, ExternalID: id, ParentID: parent, Title: description, Fields: map[string]any{"academic_year": year, "term": term, "entry_type": "practice", "identity_source": "practice_text_hash"}})
	}
	return out, nil
}
