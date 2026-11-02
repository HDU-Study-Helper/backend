package ucampus

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"academic/backend/internal/contract"
	"github.com/coder/websocket"
)

const sso = "https://sso.unipus.cn"
const socketHost = "https://umcs.unipus.cn"

type authSocket struct {
	conn    *websocket.Conn
	scope   contract.Scope
	expires time.Time
	busy    bool
}

func (a *adapter) cleanupLocked() {
	for id, s := range a.sockets {
		if !time.Now().Before(s.expires) {
			if s.conn != nil {
				s.conn.CloseNow()
			}
			delete(a.sockets, id)
		}
	}
}
func (a *adapter) CancelAuth(_ context.Context, scope contract.Scope) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, s := range a.sockets {
		if s.scope == scope {
			if s.conn != nil {
				s.conn.CloseNow()
			}
			delete(a.sockets, id)
		}
	}
	return nil
}
func (a *adapter) Close(_ context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, s := range a.sockets {
		if s.conn != nil {
			s.conn.CloseNow()
		}
		delete(a.sockets, id)
	}
	return nil
}
func (a *adapter) dropSocket(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s := a.sockets[id]; s != nil {
		if s.conn != nil {
			s.conn.CloseNow()
		}
		delete(a.sockets, id)
	}
}

type qrState struct {
	Device  string                    `json:"device"`
	SID     string                    `json:"sid"`
	URL     string                    `json:"url"`
	Status  string                    `json:"status"`
	Expires time.Time                 `json:"expires"`
	Cookies map[string][]*http.Cookie `json:"cookies"`
}

func qrResult(s state) contract.AuthResult {
	b, _ := json.Marshal(s)
	q := s.Challenge
	r := contract.AuthResult{Status: q.Status, ExpiresAt: q.Expires, PollAfter: 3 * time.Second, Session: b}
	if q.Status == "pending" || q.Status == "scanned" {
		r.QRURL = q.URL
	}
	return r
}
func qrClient(a *adapter, q *qrState) *http.Client {
	c := a.client
	jar := newTrackedJar()
	c.Jar = jar
	for origin, cs := range q.Cookies {
		if origin != sso && origin != socketHost {
			continue
		}
		u, _ := url.Parse(origin)
		jar.SetCookies(u, cs)
	}
	return &c
}
func saveQRJar(c *http.Client, q *qrState) {
	q.Cookies = map[string][]*http.Cookie{}
	for _, origin := range []string{sso, socketHost} {
		q.Cookies[origin] = c.Jar.(*trackedJar).snapshot(origin)
	}
}
func (a *adapter) BeginAuth(ctx context.Context, scope contract.Scope) (contract.AuthResult, error) {
	if err := validate(ctx, scope); err != nil {
		return contract.AuthResult{}, err
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return contract.AuthResult{}, contract.ErrUpstream
	}
	q := &qrState{Device: hex.EncodeToString(random), Status: "pending", Expires: time.Now().Add(2 * time.Minute)}
	c := qrClient(a, q)
	payload, _ := json.Marshal(map[string]string{"deviceId": q.Device, "service": portal})
	b, err := qrRequest(ctx, c, http.MethodPost, sso+"/sso/7.0/sso/code", payload, "application/json")
	if err != nil {
		return contract.AuthResult{}, err
	}
	var response struct {
		Code json.RawMessage `json:"code"`
		RS   struct {
			Content string `json:"content"`
		} `json:"rs"`
	}
	if json.Unmarshal(b, &response) != nil || text(response.Code) != "0" {
		return contract.AuthResult{}, contract.ErrParse
	}
	u, err := url.Parse(response.RS.Content)
	if err != nil || u.Scheme != "https" || u.Host != "sso.unipus.cn" || u.Path != "/sso/mobile-default.html" || !strings.HasPrefix(u.Query().Get("target"), q.Device+"@"+portal+"@") {
		return contract.AuthResult{}, contract.ErrParse
	}
	q.URL = u.String()
	a.mu.Lock()
	a.cleanupLocked()
	for id, old := range a.sockets {
		if old.scope.UserID == scope.UserID {
			if old.conn != nil {
				old.conn.CloseNow()
			}
			delete(a.sockets, id)
		}
	}
	if len(a.sockets) >= 128 {
		a.mu.Unlock()
		return contract.AuthResult{}, contract.ErrRateLimited
	}
	reserved := &authSocket{scope: scope, expires: q.Expires, busy: true}
	a.sockets[q.Device] = reserved
	a.mu.Unlock()
	committed := false
	defer func() {
		if !committed {
			a.dropSocket(q.Device)
		}
	}()
	// Reserve capacity while the bounded handshake runs. No background reader is started.
	v := url.Values{"EIO": {"4"}, "transport": {"websocket"}, "appId": {"1000"}, "openId": {"sso"}, "devId": {q.Device}}
	dialctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	ws, resp, e := websocket.Dial(dialctx, "wss://umcs.unipus.cn/umcs/?"+v.Encode(), &websocket.DialOptions{HTTPClient: c, HTTPHeader: http.Header{"Origin": {portal}, "Referer": {portal + "/"}}})
	if e != nil {
		cancel()
		if ctx.Err() != nil {
			return contract.AuthResult{}, ctx.Err()
		}
		if resp != nil && resp.StatusCode == 429 {
			return contract.AuthResult{}, contract.ErrRateLimited
		}
		return contract.AuthResult{}, contract.ErrUpstream
	}
	ws.SetReadLimit(128 << 10)
	_, hello, e := ws.Read(dialctx)
	var handshake struct {
		SID string `json:"sid"`
	}
	if e != nil || len(hello) < 2 || hello[0] != '0' || json.Unmarshal(hello[1:], &handshake) != nil || !validID(handshake.SID) {
		ws.CloseNow()
		cancel()
		return contract.AuthResult{}, contract.ErrParse
	}
	q.SID = handshake.SID
	e = ws.Write(dialctx, websocket.MessageText, []byte("40/sso,"))
	cancel()
	if e != nil {
		ws.CloseNow()
		return contract.AuthResult{}, contract.ErrUpstream
	}
	a.mu.Lock()
	if a.sockets[q.Device] != reserved {
		a.mu.Unlock()
		ws.CloseNow()
		return contract.AuthResult{}, contract.ErrAuthRequired
	}
	reserved.conn = ws
	reserved.busy = false
	committed = true
	a.mu.Unlock()
	saveQRJar(c, q)
	return qrResult(state{Version: 1, Scope: scope, Challenge: q}), nil
}
func (a *adapter) PollAuth(ctx context.Context, scope contract.Scope, raw contract.Session) (contract.AuthResult, error) {
	if err := validate(ctx, scope); err != nil {
		return contract.AuthResult{}, err
	}
	var s state
	if len(raw) > 128<<10 || json.Unmarshal(raw, &s) != nil || s.Version != 1 || s.Scope != scope || s.Challenge == nil {
		return contract.AuthResult{}, contract.ErrInvalid
	}
	q := s.Challenge
	if !validID(q.Device) || (q.SID != "" && !validID(q.SID)) {
		return contract.AuthResult{}, contract.ErrInvalid
	}
	switch q.Status {
	case "authorized", "expired":
		return qrResult(s), nil
	case "pending", "scanned":
	default:
		return contract.AuthResult{}, contract.ErrInvalid
	}
	if !time.Now().Before(q.Expires) {
		a.dropSocket(q.Device)
		q.Status = "expired"
		return qrResult(s), nil
	}
	c := qrClient(a, q)
	a.mu.Lock()
	a.cleanupLocked()
	active := a.sockets[q.Device]
	if active == nil || active.scope != scope {
		a.mu.Unlock()
		q.Status = "expired"
		return qrResult(s), nil
	}
	if active.busy {
		a.mu.Unlock()
		return contract.AuthResult{}, contract.ErrRateLimited
	}
	active.busy = true
	a.mu.Unlock()
	defer func() { a.mu.Lock(); active.busy = false; a.mu.Unlock() }()
	pollctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	_, b, err := active.conn.Read(pollctx)
	if err != nil {
		a.dropSocket(q.Device)
		if ctx.Err() != nil {
			return contract.AuthResult{}, ctx.Err()
		}
		return contract.AuthResult{}, contract.ErrUpstream
	}
	packets := strings.Split(string(b), "\x1e")
	if len(packets) > 64 {
		return contract.AuthResult{}, contract.ErrParse
	}
	for _, packet := range packets {
		switch {
		case packet == "2":
			if err = active.conn.Write(pollctx, websocket.MessageText, []byte("3")); err != nil {
				a.dropSocket(q.Device)
				return contract.AuthResult{}, contract.ErrUpstream
			}
		case packet == "1" || strings.HasPrefix(packet, "41/sso"):
			q.Status = "expired"
		case strings.HasPrefix(packet, "40/sso,") || packet == "6":
		case strings.HasPrefix(packet, "42/sso,"):
			var event []json.RawMessage
			if json.Unmarshal([]byte(strings.TrimPrefix(packet, "42/sso,")), &event) != nil || len(event) != 2 {
				return contract.AuthResult{}, contract.ErrParse
			}
			if text(event[0]) != "msg_sso" {
				continue
			}
			data := event[1]
			// Some Socket.IO producers JSON-encode the event payload as a string.
			if len(data) > 0 && data[0] == '"' {
				var value string
				if json.Unmarshal(data, &value) != nil {
					return contract.AuthResult{}, contract.ErrParse
				}
				data = []byte(value)
			}
			var message map[string]json.RawMessage
			if json.Unmarshal(data, &message) != nil || message == nil {
				return contract.AuthResult{}, contract.ErrParse
			}
			switch text(message["eventType"]) {
			case "code":
				switch text(message["status"]) {
				case "7202":
					q.Status = "scanned"
				case "7201", "7205":
					q.Status = "expired"
				default:
					return contract.AuthResult{}, contract.ErrParse
				}
			case "login":
				ticket := text(message["ticket"])
				openID := text(message["openId"])
				if ticket == "" || openID == "" {
					return contract.AuthResult{}, contract.ErrParse
				}
				v := url.Values{"service": {portal}, "ticket": {ticket}}
				body, e := qrRequest(ctx, c, http.MethodGet, sso+"/sso/serviceTicket/validate?"+v.Encode(), nil, "")
				if e != nil {
					return contract.AuthResult{}, e
				}
				var exchange struct {
					Code json.RawMessage `json:"code"`
					RS   struct {
						JWT string `json:"jwt"`
					} `json:"rs"`
				}
				if json.Unmarshal(body, &exchange) != nil || text(exchange.Code) != "0" || exchange.RS.JWT == "" {
					return contract.AuthResult{}, contract.ErrParse
				}
				// Validate the official exchanged JWT against the portal before authorized.
				input, _ := json.Marshal(SessionInput{Authorization: exchange.RS.JWT, OpenID: openID})
				verified, e := ImportSessionWithDependencies(ctx, scope, input, contract.Dependencies{HTTPClient: &a.client})
				if e != nil {
					return contract.AuthResult{}, e
				}
				var authorized state
				if json.Unmarshal(verified, &authorized) != nil {
					return contract.AuthResult{}, contract.ErrParse
				}
				authorized.Challenge = q
				q.Status = "authorized"
				saveQRJar(c, q)
				a.dropSocket(q.Device)
				return qrResult(authorized), nil
			default:
				return contract.AuthResult{}, contract.ErrParse
			}
		default:
			return contract.AuthResult{}, contract.ErrParse
		}
	}
	if q.Status == "expired" {
		a.dropSocket(q.Device)
	}
	saveQRJar(c, q)
	return qrResult(s), nil
}
func qrRequest(ctx context.Context, c *http.Client, method, endpoint string, body []byte, media string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, contract.ErrInvalid
	}
	if media != "" {
		req.Header.Set("Content-Type", media)
	}
	req.Header.Set("Origin", portal)
	req.Header.Set("Referer", portal+"/")
	resp, err := c.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, contract.ErrUpstream
	}
	defer resp.Body.Close()
	if resp.StatusCode == 400 && strings.HasPrefix(endpoint, socketHost+"/umcs/") {
		return nil, fmt.Errorf("%w: official gateway rejects HTTP polling; WebSocket transport required", contract.ErrUnsupported)
	}
	switch {
	case resp.StatusCode == 429:
		return nil, contract.ErrRateLimited
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return nil, contract.ErrAuthRequired
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, contract.ErrUpstream
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 128<<10+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, contract.ErrUpstream
	}
	if len(b) > 128<<10 {
		return nil, fmt.Errorf("%w: oversized auth packet", contract.ErrParse)
	}
	return b, nil
}
