package mooc

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"academic/backend/internal/contract"
)

type dwrBrokenBody struct{}

func (dwrBrokenBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (dwrBrokenBody) Close() error             { return nil }

func TestPostDWRRetryTransientRecovery(t *testing.T) {
	for _, method := range []string{"getPostDetailById", "getPaginationReplys"} {
		for _, failure := range []string{"transport", "body", "408", "502", "503", "504"} {
			t.Run(method+"/"+failure, func(t *testing.T) {
				calls := 0
				h, co, _ := discussionFixture(t, func(r *http.Request) (*http.Response, error) {
					calls++
					if calls == 2 {
						return response(`dwr.engine._remoteHandleCallback('0','0',{id:14});`), nil
					}
					switch failure {
					case "transport":
						return nil, errors.New("private upstream URL must not escape")
					case "body":
						res := response("")
						res.Body = dwrBrokenBody{}
						return res, nil
					default:
						res := response("")
						statuses := map[string]int{"408": 408, "502": 502, "503": 503, "504": 504}
						res.StatusCode = statuses[failure]
						return res, nil
					}
				})
				params := []string{"14"}
				if method == "getPaginationReplys" {
					params = []string{"14", "1", "1"}
				}
				got, err := readPostDWR(context.Background(), h, co, method, params)
				if err != nil || text(object(got)["id"]) != "14" || calls != 2 {
					t.Fatalf("result=%v err=%v calls=%d", got, err, calls)
				}
			})
		}
	}
}

func TestPostDWRDoesNotRetryPermanentFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"auth401", 401, "", contract.ErrAuthRequired},
		{"auth403", 403, "", contract.ErrAuthRequired},
		{"rate", 429, "", contract.ErrRateLimited},
		{"server500", 500, "", contract.ErrUpstream},
		{"invalidDWR", 200, "invalid private response", contract.ErrParse},
		{"remoteException", 200, `dwr.engine._remoteHandleException('0','0',{message:'private'});`, contract.ErrUpstream},
		{"oversize", 200, strings.Repeat("x", maxBody+1), contract.ErrParse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			h, co, _ := discussionFixture(t, func(*http.Request) (*http.Response, error) {
				calls++
				res := response(tc.body)
				res.StatusCode = tc.status
				return res, nil
			})
			_, err := readPostDWR(context.Background(), h, co, "getPostDetailById", []string{"14"})
			if !errors.Is(err, tc.want) || calls != 1 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestPostDWRRetryBoundAndSafeError(t *testing.T) {
	calls := 0
	h, co, _ := discussionFixture(t, func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("https://private.invalid/?token=secret")
	})
	_, err := readPostDWR(context.Background(), h, co, "getPostDetailById", []string{"14"})
	if err != contract.ErrUpstream || calls != 2 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestOtherDWRReadsDoNotRetry(t *testing.T) {
	for _, method := range []string{"getAllAnnouncementByTerm", "getLessonUnitLearnVo"} {
		t.Run(method, func(t *testing.T) {
			calls := 0
			h, co, _ := discussionFixture(t, func(*http.Request) (*http.Response, error) {
				calls++
				res := response("")
				res.StatusCode = 503
				return res, nil
			})
			_, err := readDWR(context.Background(), h, co, method, []string{"14"})
			if err != contract.ErrUpstream || calls != 1 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestPostDWRRetryRebuildsCookieAndToken(t *testing.T) {
	calls := 0
	h, co, _ := discussionFixture(t, func(r *http.Request) (*http.Response, error) {
		calls++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		token := "fixture"
		if calls == 2 {
			token = "updated"
		}
		cookie, err := r.Cookie("NTESSTUDYSI")
		if err != nil || cookie.Value != token || r.Header.Get("edu-script-token") != token || !strings.Contains(string(body), "httpSessionId="+token+"\n") || !strings.Contains(string(body), "c0-param0=number:14") {
			t.Fatalf("request %d did not use current credentials and regenerated body", calls)
		}
		if calls == 1 {
			res := response("", "NTESSTUDYSI=updated; Path=/; Secure")
			res.StatusCode = 503
			return res, nil
		}
		return response(`dwr.engine._remoteHandleCallback('0','0',{});`), nil
	})
	if _, err := readPostDWR(context.Background(), h, co, "getPostDetailById", []string{"14"}); err != nil || calls != 2 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestPostDWRParentCancellation(t *testing.T) {
	for _, stage := range []string{"before", "transport", "backoff", "deadline"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := error(context.Canceled)
			if stage == "before" {
				cancel()
			}
			if stage == "deadline" {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, 20*time.Millisecond)
				defer stop()
				want = context.DeadlineExceeded
			}
			calls := 0
			h, co, _ := discussionFixture(t, func(*http.Request) (*http.Response, error) {
				calls++
				if stage == "transport" {
					cancel()
				}
				if stage == "backoff" {
					time.AfterFunc(20*time.Millisecond, cancel)
				}
				return nil, io.ErrUnexpectedEOF
			})
			_, err := readPostDWR(ctx, h, co, "getPostDetailById", []string{"14"})
			wantCalls := 1
			if stage == "before" {
				wantCalls = 0
			}
			if !errors.Is(err, want) || calls != wantCalls {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestPostDWRAttemptDeadlineWithActiveParentCanRetry(t *testing.T) {
	calls := 0
	h, co, _ := discussionFixture(t, func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return nil, context.DeadlineExceeded
		}
		return response(`dwr.engine._remoteHandleCallback('0','0',{});`), nil
	})
	if _, err := readPostDWR(context.Background(), h, co, "getPostDetailById", []string{"14"}); err != nil || calls != 2 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestPostDWRCancellationErrorDoesNotRetryWithActiveParent(t *testing.T) {
	for _, bodyFailure := range []bool{false, true} {
		calls := 0
		h, co, _ := discussionFixture(t, func(*http.Request) (*http.Response, error) {
			calls++
			if bodyFailure {
				res := response("")
				res.Body = dwrCanceledBody{}
				return res, nil
			}
			return nil, context.Canceled
		})
		_, err := readPostDWR(context.Background(), h, co, "getPostDetailById", []string{"14"})
		if !errors.Is(err, context.Canceled) || calls != 1 {
			t.Fatalf("bodyFailure=%v err=%v calls=%d", bodyFailure, err, calls)
		}
	}
}

type dwrCanceledBody struct{}

func (dwrCanceledBody) Read([]byte) (int, error) { return 0, context.Canceled }
func (dwrCanceledBody) Close() error             { return nil }

func TestPostDWRInvalidInputDoesNotSendRequest(t *testing.T) {
	calls := 0
	h, co, _ := discussionFixture(t, func(*http.Request) (*http.Response, error) {
		calls++
		return nil, io.ErrUnexpectedEOF
	})
	for _, tc := range []struct {
		method string
		params []string
		want   error
	}{
		{"incrPostBrowse", []string{"14"}, contract.ErrUnsupported},
		{"getPostDetailById", nil, contract.ErrInvalid},
		{"getPostDetailById", []string{"invalid"}, contract.ErrInvalid},
		{"getPaginationReplys", []string{"14", "2", "1"}, contract.ErrInvalid},
		{"getPaginationReplys", []string{"14", "1", "0"}, contract.ErrInvalid},
	} {
		_, err := readPostDWR(context.Background(), h, co, tc.method, tc.params)
		if !errors.Is(err, tc.want) || calls != 0 {
			t.Fatalf("method=%s err=%v calls=%d", tc.method, err, calls)
		}
	}
}
