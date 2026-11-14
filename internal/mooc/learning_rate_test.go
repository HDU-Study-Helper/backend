package mooc

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestLearningRateUsesSourceTermPreservesSeparateCounters(t *testing.T) {
	calls := 0
	h, _, _ := discussionFixture(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Path != "/web/j/mocCourseV2RpcBean.getMemberLearningRate.rpc" || r.Header.Get("Referer") != origin+"/spoc/learn/10?tid=101" {
			t.Fatal(r.URL.Path, r.Header.Get("Referer"))
		}
		r.ParseForm()
		if r.Form.Get("termId") != "202" {
			t.Fatal(r.Form)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"code":0,"result":{"termId":202,"memberId":987,"lessonLearned":6,"lessonsCount":46,"cookie":"SECRET"}}`))}, nil
	})
	rows, e := readLearningRate(context.Background(), h, course{ID: "10", Term: "101", SourceTerm: "202", Type: 2, Name: "Course"})
	if e != nil || len(rows) != 1 || calls != 1 {
		t.Fatal(e, rows, calls)
	}
	f := rows[0].Fields
	if f["lessonLearned"] != int64(6) || f["lessonsCount"] != int64(46) || rows[0].ParentID != key("10", "101") {
		t.Fatal(rows)
	}
	for _, k := range []string{"memberId", "cookie", "completePercent", "status"} {
		if _, ok := f[k]; ok {
			t.Fatal("invented or leaked", k)
		}
	}
}

func TestLearningRateExplicitNullRetainsUnknownInsteadOfZero(t *testing.T) {
	h, _, _ := discussionFixture(t, func(*http.Request) (*http.Response, error) {
		return response(`{"code":0,"result":{"termId":202,"lessonLearned":null,"lessonsCount":12}}`), nil
	})
	rows, e := readLearningRate(context.Background(), h, course{ID: "10", Term: "101", SourceTerm: "202"})
	if e != nil || len(rows) != 1 {
		t.Fatal(e, rows)
	}
	f := rows[0].Fields
	if value, present := f["lessonLearned"]; !present || value != nil {
		t.Fatal("unknown became zero", f)
	}
	if f["lessonsCount"] != int64(12) || f["progress_status"] != "partial_source_fields_observed" {
		t.Fatal(f)
	}
}

func TestLearningRateRejectsMissingAndInvalidCounters(t *testing.T) {
	for _, body := range []string{`{"termId":999,"lessonLearned":6,"lessonsCount":46}`, `{"termId":202,"lessonsCount":46}`, `{"termId":202,"lessonLearned":-1,"lessonsCount":46}`, `{"termId":202,"lessonLearned":"6","lessonsCount":46}`} {
		h, _, _ := discussionFixture(t, func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"code":0,"result":` + body + `}`))}, nil
		})
		if _, e := readLearningRate(context.Background(), h, course{ID: "10", Term: "101", SourceTerm: "202"}); e == nil {
			t.Fatal(body)
		}
	}
	if n, ok := nonnegativeSourceInteger(json.Number("0")); !ok || n != 0 {
		t.Fatal("zero lost")
	}
}
