package main

import (
	"net/http"
	"time"
)

// pacedTransport shares one request lane across both adapters, including redirects
// and restored sessions. A failed request also consumes its interval; no retries
// are added. The lane is released after headers, before body decoding or storage.
type pacedTransport struct {
	base     http.RoundTripper
	interval time.Duration
	lane     chan struct{}
	next     time.Time // protected by lane
}

func newPacedTransport(base http.RoundTripper, interval time.Duration) *pacedTransport {
	return &pacedTransport{base: base, interval: interval, lane: make(chan struct{}, 1)}
}

func (p *pacedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case p.lane <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-p.lane }()
	if delay := time.Until(p.next); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	response, err := p.base.RoundTrip(req)
	p.next = time.Now().Add(p.interval)
	return response, err
}
