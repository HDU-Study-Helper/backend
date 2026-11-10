package chaoxing

import (
	"academic/backend/internal/contract"
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
)

func TestNoticeBareSuccessOnlyTerminatesContinuation(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		last       string
		want       error
	}{
		{"observed-terminal", `{"status":true}`, "previous-page", nil},
		{"ambiguous-first-page", `{"status":true}`, "", contract.ErrParse},
		{"failure", `{"status":false}`, "previous-page", contract.ErrAuthRequired},
		{"missing-status", `{}`, "previous-page", contract.ErrParse},
		{"null-list", `{"status":true,"notices":{"list":null,"lastPage":1}}`, "previous-page", contract.ErrParse},
		{"null-notices", `{"status":true,"notices":null}`, "previous-page", contract.ErrParse},
		{"unexpected-envelope", `{"status":true,"error":"unexpected"}`, "previous-page", contract.ErrParse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, tc.body) })
			_, c, _, e := a.restore(testScope, authorized(testScope))
			if e != nil {
				t.Fatal(e)
			}
			records, cursor, more, e := notices(context.Background(), c, tc.last)
			if !errors.Is(e, tc.want) {
				t.Fatalf("got %v want %v", e, tc.want)
			}
			if e == nil && (len(records) != 0 || cursor != "" || more) {
				t.Fatal("terminal page advanced cursor")
			}
		})
	}
}
