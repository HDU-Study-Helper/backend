package mooc

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"academic/backend/internal/contract"
)

func TestQRImageValidation(t *testing.T) {
	good := pngFixture(t)
	for _, tc := range []struct {
		name   string
		body   string
		status int
		want   error
	}{
		{"html", "<html>not an image</html>", 200, contract.ErrParse},
		{"oversized", strings.Repeat("x", (512<<10)+1), 200, contract.ErrParse},
		{"truncated", string(good[:len(good)/2]), 200, contract.ErrParse},
		{"redirect", "", 302, contract.ErrUpstream},
		{"limited", "", 429, contract.ErrRateLimited},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testAdapter(func(*http.Request) (*http.Response, error) {
				r := response(tc.body)
				r.StatusCode = tc.status
				r.Header.Set("Location", "https://example.invalid/image")
				return r, nil
			})
			u, _ := url.Parse("https://mooc-image.nosdn.127.net/fixture.png")
			_, _, e := a.downloadQR(context.Background(), u)
			if !errors.Is(e, tc.want) {
				t.Fatal(e)
			}
		})
	}
	a := testAdapter(func(*http.Request) (*http.Response, error) { t.Fatal("untrusted image URL requested"); return nil, nil })
	for _, raw := range []string{"https://example.invalid/q.png", "http://mooc-image.nosdn.127.net/q.png", "https://mooc-image.nosdn.127.net.evil.invalid/q.png", "https://user@mooc-image.nosdn.127.net/q.png", "https://mooc-image.nosdn.127.net:443/q.png"} {
		u, _ := url.Parse(raw)
		_, _, e := a.downloadQR(context.Background(), u)
		if !errors.Is(e, contract.ErrParse) {
			t.Fatal(raw, e)
		}
	}
}
