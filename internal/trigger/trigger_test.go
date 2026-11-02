package trigger

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestBusinessChangeEligibility(t *testing.T) {
	base := Change{UserID: "owner", RecordID: "record", Source: "chaoxing", Resource: "discussion", Operation: Updated}
	if !Eligible(base) {
		t.Fatal("business change excluded")
	}
	for _, resource := range []string{"session", "cursor", "lease", "job", "sync_status", "message"} {
		c := base
		c.Resource = resource
		if resource == "message" {
			c.Source = "dws"
		}
		if Eligible(c) {
			t.Fatal("infrastructure or DWS message eligible", resource)
		}
	}
	c := base
	c.Source = "dws"
	c.Resource = "attachment"
	if !Eligible(c) {
		t.Fatal("attachment change excluded")
	}
	c = base
	c.UserID = ""
	if Eligible(c) {
		t.Fatal("ownerless event accepted")
	}
	if e := (Noop{}).Handle(context.Background(), base); e != nil {
		t.Fatal(e)
	}
}
func TestChangedFieldsIgnoreObservationAndKeepLargeIdentifiers(t *testing.T) {
	a := json.RawMessage(`{"title":"a","fields":{"id":9007199254740992},"observed_at":"old"}`)
	b := json.RawMessage(`{"observed_at":"new","fields":{"id":9007199254740993},"title":"b"}`)
	if got := FieldChanges(a, b); !reflect.DeepEqual(got, []string{"fields", "title"}) {
		t.Fatal(got)
	}
	if got := FieldChanges(json.RawMessage(`{"fields":{"a":1,"b":2}}`), json.RawMessage(`{"fields":{"b":2,"a":1}}`)); len(got) != 0 {
		t.Fatal("object key order counted as change", got)
	}
}
