package agent

import (
	f "academic/agent/framework"
	"context"
	"testing"
)

type keywordDataStub struct {
	f.DataService
	scope f.Scope
	q     f.KeywordSearchRequest
}

func (d *keywordDataStub) SearchKeywords(_ context.Context, s f.Scope, q f.KeywordSearchRequest) (f.KeywordSearchResult, error) {
	d.scope = s
	d.q = q
	return f.KeywordSearchResult{Hits: []f.KeywordSearchHit{{Dataset: "schedules", Row: map[string]any{"title": "数学分析"}}}, Coverage: []f.KeywordSearchCoverage{{Dataset: "schedules", Matched: 1, Complete: true}}}, nil
}
func TestKeywordSearchCrossesBackendBridgeWithTrustedOwner(t *testing.T) {
	d := &keywordDataStub{}
	b := BackendBridge{Port: BackendServices{Data: d}}
	got, err := b.SearchKeywords(context.Background(), f.Scope{OwnerID: "owner", AgentID: "learner"}, f.KeywordSearchRequest{Keywords: []string{"数分"}, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if d.scope.OwnerID != "owner" || len(got.Hits) != 1 || got.Hits[0].Dataset != "schedules" || len(d.q.Keywords) != 1 {
		t.Fatal(got, d.scope, d.q)
	}
}
