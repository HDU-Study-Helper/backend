package mooc

import (
	"academic/backend/internal/contract"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestDWRConstructionBudgetRejectsUnreachableSparseArrays(t *testing.T) {
	// Five tiny statements would construct >10k slots despite returning an empty
	// result. The global budget must apply before sparse backing arrays allocate.
	var b strings.Builder
	for i := 0; i < 5; i++ {
		fmt.Fprintf(&b, "var s%d=[];s%d[2000]=null;", i, i)
	}
	b.WriteString("dwr.engine._remoteHandleCallback('0','0',[]);")
	if _, e := parseDWR(b.String()); !errors.Is(e, contract.ErrParse) {
		t.Fatal("unreachable sparse allocations bypassed construction bound", e)
	}
	if _, e := dwrSplit(strings.Repeat("[", 25)+strings.Repeat("]", 25), ';'); !errors.Is(e, contract.ErrParse) {
		t.Fatal("split nesting unbounded")
	}
	if _, e := dwrSplit(strings.Repeat(";", 20001), ';'); !errors.Is(e, contract.ErrParse) {
		t.Fatal("split statements unbounded")
	}
}
func TestDataOnlyDWRParsing(t *testing.T) {
	body := `//dwr response
 var s0={};var s1={};s0.id=12;s0.title="Notice";s0.content="<p>Read</p>";s1.id=13;s1.title='It\'s due';dwr.engine._remoteHandleCallback('0','0',[s0,s1]);`
	result, e := parseDWR(body)
	if e != nil {
		t.Fatal(e)
	}
	rows, ok := result.([]any)
	if !ok || len(rows) != 2 || text(object(rows[0])["id"]) != "12" || text(object(rows[1])["title"]) != "It's due" {
		t.Fatal(result)
	}
	for _, bad := range []string{`alert("x");dwr.engine._remoteHandleCallback('0','0',[]);`, `var s0={};s0.content=evil();dwr.engine._remoteHandleCallback('0','0',s0);`, `var s0={};var s0={};dwr.engine._remoteHandleCallback('0','0',s0);`, `dwr.engine._remoteHandleCallback('0','0',s1);`, `var s0={};s0.x=1;`} {
		if _, e := parseDWR(bad); !errors.Is(e, contract.ErrParse) {
			t.Fatal("executable/invalid DWR accepted", bad, e)
		}
	}
}
func TestDWRDeferredArraysCyclesAndEscapedBodies(t *testing.T) {
	result, e := parseDWR(`var s0={};var s1=[];s0.items=s1;s1[0]="ok";s0.content='\x3Cp\x3E\uD83D\uDE00';s0.gmtModified=new Date(100);dwr.engine._remoteHandleCallback('0','0',s0);`)
	if e != nil {
		t.Fatal(e)
	}
	m := object(result)
	if len(m["items"].([]any)) != 1 || m["content"] != "<p>😀" {
		t.Fatal(result)
	}
	for _, bad := range []string{`var s0={};s0.self=s0;dwr.engine._remoteHandleCallback('0','0',s0);`, `var s0={};var s1=[];s0.items=s1;s1[0]=s0;dwr.engine._remoteHandleCallback('0','0',s0);`} {
		if _, e := parseDWR(bad); !errors.Is(e, contract.ErrParse) {
			t.Fatal("cyclic data accepted", e)
		}
	}
}
func TestPageDataIgnoresNestedIDsAndQuotedText(t *testing.T) {
	co := course{ID: "10", Term: "101", Type: 2}
	got, e := discoverPage(`window.courseDto={id:"10",schoolPanel:{id:99},description:"id:999"};window.termDto={id:"101",nested:{id:888}};window.moocTermDto={id:"202"};`, co)
	if e != nil || got.ActiveTerm != "202" {
		t.Fatal(got, e)
	}
}
func TestPlainUnitTwoIsSubtitle(t *testing.T) {
	dto := fixtureDTO(t, `{"id":101,"chapters":[{"id":1,"lessons":[{"id":2,"units":[{"id":3,"contentType":2}]}]}]}`)
	co := course{ID: "10", Term: "101", Type: 1}
	rows, e := termRecords(dto, co, contract.Assignment)
	if e != nil || len(rows) != 0 {
		t.Fatal(rows, e)
	}
	rows, e = termRecords(dto, co, "material")
	if e != nil || len(rows) != 1 || rows[0].Fields["unit_type"] != "video_subtitles" {
		t.Fatal(rows, e)
	}
}
