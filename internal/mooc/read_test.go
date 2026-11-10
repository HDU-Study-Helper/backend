package mooc

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"academic/backend/internal/contract"
)

func TestNoticeSyncExactReadAndOwner(t *testing.T) {
	a := testAdapter(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/dwr/call/plaincall/CourseBean.getAllAnnouncementByTerm.dwr" {
			t.Fatal("unexpected endpoint", req.URL.Path)
		}
		b, _ := io.ReadAll(req.Body)
		body := string(b)
		if req.Header.Get("Content-Type") != "text/plain" || !strings.Contains(body, "c0-param0=number:202\n") || !strings.Contains(body, "scriptSessionId=${scriptSessionId}190") || strings.Contains(body, "%24") {
			t.Fatal("wrong DWR serialization")
		}
		return response(`var s0={};s0.id=1;s0.termId=202;s0.title="Notice";s0.content="<p>Read this</p>";s0.publishTime=new Date(100);dwr.engine._remoteHandleCallback('0','0',[s0]);`), nil
	})
	s := sessionFor(t, a, scope, "fixture")
	c, _ := newCursor(scope, contract.Notice, "")
	c.Done = true
	c.Courses = []course{{ID: "10", Term: "101", SourceTerm: "202", ActiveTerm: "202", ActiveRead: true, Type: 2, Discovered: true, Name: "Owner"}}
	b, _ := json.Marshal(c)
	p, e := a.Sync(context.Background(), scope, s, contract.Notice, base64Cursor(b))
	if e != nil || p.HasMore || len(p.Records) != 1 {
		t.Fatal(p, e)
	}
	row := p.Records[0]
	if row.ParentID != key("10", "101") || row.Content != "Read this" || row.Fields["source_scope"] != "active_source" || row.Fields["publishTime"] != json.Number("100") {
		t.Fatal(row)
	}
}
func TestBodyReaderRejectsAssessmentBeforeNetwork(t *testing.T) {
	a := testAdapter(func(*http.Request) (*http.Response, error) {
		t.Fatal("assessment body must not request network")
		return nil, nil
	})
	_, h, _ := a.restore(scope, sessionFor(t, a, scope, "fixture"))
	for _, typ := range []string{"1", "2", "5", "7", "15", "unknown"} {
		if _, e := readUnitBody(context.Background(), h, course{ID: "10", Term: "101", Type: 1}, "12", typ, "13"); e != contract.ErrUnsupported {
			t.Fatal(typ, e)
		}
	}
}
