package mooc

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"academic/backend/internal/contract"
)

func TestNewExamVerifiedEmptyAndUnknownResponses(t *testing.T) {
	for _, tc := range []struct {
		body string
		want error
	}{
		{`{"code":0,"result":[]}`, nil},
		{`{"code":0,"result":[{"id":12,"questions":["private"]}]}`, contract.ErrUnsupported},
		{`{"code":0,"result":null}`, nil},
		{`{"code":0,"result":{}}`, contract.ErrParse},
		{`{"code":0}`, contract.ErrParse},
		{`{"code":"0","result":[]}`, contract.ErrParse},
		{`{"code":0,"result":[]} {}`, contract.ErrParse},
		{`{"code":1}`, contract.ErrAuthRequired},
		{`{"code":-1007}`, contract.ErrAuthRequired},
		{`{"code":2}`, contract.ErrUpstream},
	} {
		rows, err := parseNewExams([]byte(tc.body))
		if !errors.Is(err, tc.want) || len(rows) != 0 || err == nil && rows == nil {
			t.Fatalf("body=%s rows=%v err=%v want=%v", tc.body, rows, err, tc.want)
		}
	}
}

func TestExamSupplementalStageKeepsOldRowsAndBothSourceTerms(t *testing.T) {
	calls := []string{}
	fail := true
	a := testAdapter(func(req *http.Request) (*http.Response, error) {
		if err := req.ParseForm(); err != nil {
			t.Fatal(err)
		}
		term := req.Form.Get("termId")
		if req.Header.Get("Referer") != origin+"/spoc/learn/10?tid=101" || len(req.PostForm) != 1 {
			t.Fatal("source request lost route or exact form", req.Form)
		}
		if req.URL.Query().Get("csrfKey") != "fixture" || req.Header.Get("edu-script-token") != "fixture" || req.Header.Get("Content-Type") != "application/x-www-form-urlencoded;charset=UTF-8" {
			t.Fatal("incorrect authenticated form request")
		}
		switch req.URL.Path {
		case "/web/j/courseBean.getLastLearnedMocTermDto.rpc":
			calls = append(calls, "catalog:"+term)
			return response(`{"code":0,"result":{"mocTermDto":{"id":` + term + `,"chapters":[],"exams":[{"id":19,"name":"Existing exam","totalScore":100}]}}}`), nil
		case "/web/j/courseBean.getNewExamList.rpc":
			calls = append(calls, "new:"+term)
			if fail {
				return response(`{"code":0,"result":[{"unverified":true}]}`), nil
			}
			if term == "101" {
				return response(`{"code":0,"result":null}`), nil
			}
			return response(`{"code":0,"result":[]}`), nil
		default:
			t.Fatal("unexpected source request", req.URL.Path)
			return nil, nil
		}
	})
	s := sessionFor(t, a, scope, "fixture")
	co := course{ID: "10", Term: "101", Type: 2, Discovered: true, ActiveTerm: "202"}
	c, _ := newCursor(scope, contract.Exam, "")
	c.Courses, c.Done = []course{co}, true
	p, err := a.Sync(context.Background(), scope, s, contract.Exam, policyCursor(t, c))
	if err != nil || !p.HasMore || len(p.Records) != 1 || p.Records[0].Title != "Existing exam" {
		t.Fatal("old exam not emitted before new source", p, err)
	}
	oldID := p.Records[0].ExternalID
	saved, err := newCursor(scope, contract.Exam, p.NextCursor)
	if err != nil || !saved.Courses[0].SupplementalPending || saved.Courses[0].ActiveRead {
		t.Fatal("source advanced before supplemental read", saved, err)
	}
	failed, err := a.Sync(context.Background(), scope, p.Session, contract.Exam, p.NextCursor)
	if !errors.Is(err, contract.ErrUnsupported) || len(failed.Records) != 0 || failed.NextCursor != "" {
		t.Fatal("unknown list advanced or reemitted old records", failed, err)
	}
	fail = false
	p, err = a.Sync(context.Background(), scope, p.Session, contract.Exam, p.NextCursor)
	if err != nil || !p.HasMore || len(p.Records) != 0 {
		t.Fatal("empty new list should only advance", p, err)
	}
	p, err = a.Sync(context.Background(), scope, p.Session, contract.Exam, p.NextCursor)
	if err != nil || !p.HasMore || len(p.Records) != 1 || p.Records[0].ExternalID == oldID || p.Records[0].ParentID != co.ref() || p.Records[0].Fields["sourceTermId"] != "202" {
		t.Fatal("active source identity lost", p, err)
	}
	p, err = a.Sync(context.Background(), scope, p.Session, contract.Exam, p.NextCursor)
	if err != nil || p.HasMore || len(p.Records) != 0 {
		t.Fatal("new list did not finish", p, err)
	}
	if strings.Join(calls, ",") != "catalog:101,new:101,new:101,catalog:202,new:202" {
		t.Fatal("unexpected reads", calls)
	}
}

func TestExamSupplementalExcludedAndCanceledWithoutRead(t *testing.T) {
	calls := 0
	a := testAdapter(func(*http.Request) (*http.Response, error) {
		calls++
		t.Fatal("unexpected read")
		return nil, nil
	})
	s := sessionFor(t, a, scope, "fixture")
	co := course{ID: "10", Term: "101", Type: 1, SupplementalPending: true}
	c, _ := newCursor(scope, contract.Exam, "")
	c.Courses, c.Done = []course{co}, true
	raw := policyCursor(t, c)
	ctx := contract.WithExcludedCourses(context.Background(), scope, []string{co.ref()})
	p, err := a.Sync(ctx, scope, s, contract.Exam, raw)
	if err != nil || p.HasMore || len(p.Records) != 0 || calls != 0 {
		t.Fatal(p, err, calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Sync(ctx, scope, s, contract.Exam, raw); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatal(err, calls)
	}
}
