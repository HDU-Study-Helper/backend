package chaoxing

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"academic/backend/internal/contract"
)

func TestDiscussionReplyUsersCancellationAfterPage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := replyFixture(t, ownedTopic, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":true,"datas":[{"id":1,"createrName":"Person"}],"poff":{"lastPage":false,"lastValue":"next"}}`)
		cancel()
	})
	p, err := a.DiscussionReplyUsers(ctx, testScope, authorized(testScope), "c0", "k0", "topic")
	if !errors.Is(err, context.Canceled) || len(p.Records) != 0 || len(p.Session) != 0 {
		t.Fatalf("partial canceled result %+v %v", p, err)
	}
}

func TestDiscussionReplyUsersTopicCountBound(t *testing.T) {
	calls := 0
	a := replyFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{"status":true,"datas":[`)
		for i := 0; i < 201; i++ {
			if i > 0 {
				fmt.Fprint(w, ",")
			}
			fmt.Fprintf(w, `{"uuid":"%d-%d","content":"private"}`, calls, i)
		}
		fmt.Fprintf(w, `],"poff":{"lastPage":false,"lastValue":"%d","lastAuxValue":""}}`, calls)
	}, func(w http.ResponseWriter, r *http.Request) { t.Fatal("reply request without bounded ownership") })
	p, err := a.DiscussionReplyUsers(context.Background(), testScope, authorized(testScope), "c0", "k0", "topic")
	if !errors.Is(err, contract.ErrParse) || calls != 2 || len(p.Records) != 0 {
		t.Fatal(p, err, calls)
	}
}
