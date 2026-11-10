package main

import (
	"context"
	"net/http"
	"testing"
)

func TestInspectRejectsTaskExamAndWriteEndpointsBeforeNetwork(t *testing.T) {
	for _, target := range []string{"https://mooc1.chaoxing.com/work/task?workId=x", "https://mooc1-api.chaoxing.com/exam/start", "https://groupweb.chaoxing.com/course/topic/bbs/delete", "https://evil.test/course/topic/topicList"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			req, _ := http.NewRequestWithContext(ctx, method, target, nil)
			if _, err := (&trace{}).RoundTrip(req); err == nil || err.Error() != "endpoint outside read-only allowlist" {
				t.Fatalf("endpoint not rejected by allowlist: %s %s", method, target)
			}
		}
	}
}
