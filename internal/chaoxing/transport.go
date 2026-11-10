package chaoxing

import (
	"bytes"
	"net/http"
	"time"
)

// pacedTransport spaces request starts, including automatic redirects. The gate
// contains timing state only, never cookies or account data, and is shared by
// this adapter's private clients to avoid bursts across resources/connections.
type pacedTransport struct {
	base     http.RoundTripper
	gate     chan struct{}
	interval time.Duration
	next     time.Time
}

func newPacedTransport(base http.RoundTripper, interval time.Duration) *pacedTransport {
	if base == nil {
		base = http.DefaultTransport
	}
	return &pacedTransport{base: base, gate: make(chan struct{}, 1), interval: interval}
}
func (p *pacedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	select {
	case p.gate <- struct{}{}:
	case <-r.Context().Done():
		return nil, r.Context().Err()
	}
	if delay := time.Until(p.next); delay > 0 {
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-r.Context().Done():
			timer.Stop()
			<-p.gate
			return nil, r.Context().Err()
		}
	}
	if e := r.Context().Err(); e != nil {
		<-p.gate
		return nil, e
	}
	p.next = time.Now().Add(p.interval)
	<-p.gate
	return p.base.RoundTrip(r)
}

// Match a verification document near its start, not arbitrary course content
// mentioning verification. URL detection handles the usual 202 response.
func verificationPage(body []byte) bool {
	prefix := body
	if len(prefix) > 8192 {
		prefix = prefix[:8192]
	}
	return bytes.Contains(prefix, []byte("操作异常，请输入图片中的验证码")) ||
		(bytes.Contains(prefix, []byte("antispiderShowVerify")) && bytes.Contains(prefix, []byte("验证码")))
}
