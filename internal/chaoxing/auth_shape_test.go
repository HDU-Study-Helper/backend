package chaoxing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"academic/backend/internal/contract"
)

// These fragments preserve the anonymous official response shapes observed
// during diagnosis. All challenge values are synthetic; no upstream bytes or
// cookies are retained. The poll message is deliberately replaced as well.
const anonymousLoginFixture = `<input type="hidden" value="fixture-uuid" id="uuid" />
<input type="hidden" value="fixture-enc" id="enc" />
<input type="hidden" id="doubleFactorLogin" name="doubleFactorLogin" value="0" />
<input type="hidden" id="forbidotherlogin" name="forbidotherlogin" value="0" />`
const anonymousPendingFixture = `{"mes":"fixture pending message","type":"3","status":false}`

func TestAnonymousOfficialAuthShapes(t *testing.T) {
	a := fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			fmt.Fprint(w, anonymousLoginFixture)
		case "/createqr":
			if r.URL.Query().Get("uuid") != "fixture-uuid" || r.URL.Query().Get("fid") != "-1" {
				t.Error("wrong QR parameters")
			}
			// The actual response declares JPEG but has a PNG magic signature.
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write([]byte("\x89PNG\r\n\x1a\nfixture"))
		case "/getauthstatus/v2":
			if r.Method != http.MethodPost {
				t.Error("wrong polling method")
			}
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("enc") != "fixture-enc" || r.Form.Get("uuid") != "fixture-uuid" {
				t.Error("missing challenge")
			}
			fmt.Fprint(w, anonymousPendingFixture)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	first, err := a.BeginAuth(context.Background(), testScope)
	if err != nil || first.Status != "pending" || first.QRMediaType != "image/png" {
		t.Fatalf("begin status=%s media=%s err=%v", first.Status, first.QRMediaType, err)
	}
	next, err := a.PollAuth(context.Background(), testScope, first.Session)
	if err != nil || next.Status != "pending" || !next.ExpiresAt.Equal(first.ExpiresAt) {
		t.Fatalf("pending status=%s err=%v", next.Status, err)
	}
}

func TestQRStatusCodeShapes(t *testing.T) {
	for _, tc := range []struct {
		code, status string
		want         error
	}{
		{`3`, "pending", nil}, {`"3"`, "pending", nil},
		{`"0"`, "pending", nil}, {`"1"`, "pending", nil}, {`"2"`, "pending", nil},
		{`"4"`, "scanned", nil}, {`"6"`, "expired", nil}, {`"7"`, "", contract.ErrAuthRequired},
		{`"8"`, "", contract.ErrParse}, {`8`, "", contract.ErrParse},
		{`null`, "", contract.ErrParse}, {`true`, "", contract.ErrParse},
		{`{}`, "", contract.ErrParse}, {`[]`, "", contract.ErrParse},
		{`3.0`, "", contract.ErrParse}, {`"3.0"`, "", contract.ErrParse},
		{`"03"`, "", contract.ErrParse}, {`" 3"`, "", contract.ErrParse},
		{`"+3"`, "", contract.ErrParse}, {`""`, "", contract.ErrParse},
	} {
		t.Run(tc.code, func(t *testing.T) {
			a := fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, `{"status":false,"type":%s}`, tc.code) })
			session, _ := json.Marshal(state{Version: 1, Scope: testScope, UUID: "fixture", Enc: "fixture", Expires: time.Now().Add(time.Minute)})
			result, err := a.PollAuth(context.Background(), testScope, session)
			if !errors.Is(err, tc.want) || result.Status != tc.status {
				t.Fatalf("status=%s err=%v", result.Status, err)
			}
		})
	}
}

func TestHiddenNameFallback(t *testing.T) {
	fields, err := hidden([]byte(`<input name="uuid" value="fixture-uuid"><input name="enc" value="fixture-enc">`))
	if err != nil || fields["uuid"] != "fixture-uuid" || fields["enc"] != "fixture-enc" {
		t.Fatal("name-only input parsing failed")
	}
}
