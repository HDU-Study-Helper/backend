// Package chaoxing implements bounded, read-only Chaoxing observations.
package chaoxing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"

	"academic/backend/internal/contract"
	"golang.org/x/net/publicsuffix"
)

const passport = "https://passport2.chaoxing.com"
const api = "https://mooc1-api.chaoxing.com"
const middle = "https://mooc1.chaoxing.com/visit/stucoursemiddle"
const login = passport + "/login?fid=-1&refer=https%3A%2F%2Fi.chaoxing.com"
const maxBody = 4 << 20

type adapter struct{ client http.Client }

var _ contract.Adapter = (*adapter)(nil)

func New(deps contract.Dependencies) (contract.Adapter, error) {
	c := http.Client{}
	if deps.HTTPClient != nil {
		c = *deps.HTTPClient
	}
	c.Jar = nil
	if c.Timeout <= 0 || c.Timeout > 30*time.Second {
		c.Timeout = 30 * time.Second
	}
	// Reject cross-platform redirects and stop at login pages. No request URL or
	// upstream response body is interpolated into errors.
	c.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) >= 8 || !official(r.URL) {
			return contract.ErrUpstream
		}
		if strings.Contains(r.URL.Hostname(), "passport2.") {
			return http.ErrUseLastResponse
		}
		return nil
	}
	c.Transport = newPacedTransport(c.Transport, 700*time.Millisecond)
	return &adapter{client: c}, nil
}
func (*adapter) Describe() contract.Descriptor {
	return contract.Descriptor{Source: "chaoxing", Name: "学习通", QRLogin: true,
		Resources:   []contract.Resource{contract.Course, contract.Chapter, contract.Lesson, contract.Assignment, contract.Exam, contract.Progress, contract.Notice, discussionResource, contract.Material},
		Limitations: []string{"Discussion probe read 11 posts from one authorized course; other courses and live continuation remain unverified", "Full rescan; discussion reads one course page per call with explicit poff pagination; assignments require total-count evidence and read at most five safe headers per call", "Teaching nodes retain native IDs and explicit personal task-point evidence; title-only groups are path context; exam is account-level schedule only", "Materials are directory metadata only; no file content or download requests; live traversal remains unverified", "No answers, submissions, progress writes, attachment downloads, or two-factor login"}}
}

type cookieEntry struct {
	Origin string
	Cookie http.Cookie
}
type jar struct {
	base    http.CookieJar
	Entries []cookieEntry
}

func (j *jar) Cookies(u *url.URL) []*http.Cookie { return j.base.Cookies(u) }
func (j *jar) SetCookies(u *url.URL, cs []*http.Cookie) {
	j.base.SetCookies(u, cs)
	for _, c := range cs {
		// Max-Age takes precedence over Expires. Convert its relative lifetime
		// once while observing Set-Cookie; restore clears positive MaxAge.
		if c.MaxAge > 0 {
			copyCookie := *c
			copyCookie.Expires = time.Now().Add(time.Duration(c.MaxAge) * time.Second)
			c = &copyCookie
		}
		origin := u.Scheme + "://" + u.Host + u.EscapedPath()
		if c.Path == "" {
			p := u.Path
			i := strings.LastIndex(p, "/")
			if i <= 0 {
				c2 := *c
				c2.Path = "/"
				c = &c2
			} else {
				c2 := *c
				c2.Path = p[:i]
				c = &c2
			}
		}
		key := cookieKey(u, c)
		found := false
		for i, e := range j.Entries {
			eu, _ := url.Parse(e.Origin)
			if cookieKey(eu, &e.Cookie) == key {
				j.Entries[i] = cookieEntry{origin, *c}
				found = true
				break
			}
		}
		if !found {
			j.Entries = append(j.Entries, cookieEntry{origin, *c})
		}
	}
}

func cookieKey(u *url.URL, c *http.Cookie) string {
	domain := strings.TrimPrefix(strings.ToLower(c.Domain), ".")
	if domain == "" {
		domain = "host:" + u.Hostname()
	}
	return c.Name + "\x00" + domain + "\x00" + c.Path
}

type state struct {
	Version      int
	Scope        contract.Scope
	Cookies      []cookieEntry
	UUID         string
	Enc          string
	DoubleFactor string
	ForbidOther  string
	Expires      time.Time
	Authorized   bool
}

func validScope(s contract.Scope) bool {
	return s.UserID != "" && s.ConnectionID != "" && len(s.UserID) <= 512 && len(s.ConnectionID) <= 512
}
func (a *adapter) restore(scope contract.Scope, session contract.Session) (*state, *http.Client, *jar, error) {
	if !validScope(scope) {
		return nil, nil, nil, contract.ErrInvalid
	}
	s := &state{Version: 1, Scope: scope}
	if len(session) > 0 {
		if len(session) > 1<<20 || json.Unmarshal(session, s) != nil || s.Version != 1 || s.Scope != scope || len(s.Cookies) > 256 {
			return nil, nil, nil, contract.ErrAuthRequired
		}
	}
	b, _ := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	j := &jar{base: b}
	for _, e := range s.Cookies {
		// k8s is a transient routing ticket. Replaying an old persisted value
		// triggers the official anti-spider page even with valid login cookies.
		if e.Cookie.Name == "k8s" {
			continue
		}
		u, err := url.Parse(e.Origin)
		if err != nil || !official(u) {
			return nil, nil, nil, contract.ErrAuthRequired
		}
		// Stored MaxAge is a relative lifetime. Preserve absolute Expires rather
		// than renewing it on each restore; MaxAge<0 still deletes the cookie.
		c := e.Cookie
		if c.MaxAge > 0 {
			c.MaxAge = 0
		}
		j.SetCookies(u, []*http.Cookie{&c})
	}
	c := a.client
	c.Jar = j
	return s, &c, j, nil
}
func save(s *state, j *jar) contract.Session {
	s.Cookies = make([]cookieEntry, 0, len(j.Entries))
	for _, entry := range j.Entries {
		if entry.Cookie.Name != "k8s" {
			s.Cookies = append(s.Cookies, entry)
		}
	}
	b, _ := json.Marshal(s)
	return b
}
func official(u *url.URL) bool {
	h := u.Hostname()
	return u.Scheme == "https" && (h == "chaoxing.com" || strings.HasSuffix(h, ".chaoxing.com")) && u.User == nil && (u.Port() == "" || u.Port() == "443")
}
func request(ctx context.Context, c *http.Client, method, target string, form url.Values) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	r, e := http.NewRequestWithContext(ctx, method, target, body)
	if e != nil || !official(r.URL) {
		return nil, contract.ErrInvalid
	}
	r.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	r.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	r.Header.Set("Accept", "*/*")
	if strings.HasPrefix(target, passport+"/createqr?") {
		r.Header.Set("Accept", "image/*,*/*")
	} else if target == passport+"/getauthstatus/v2" {
		r.Header.Set("Accept", "application/json,*/*")
	}
	r.Header.Set("Referer", login)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("X-Requested-With", "XMLHttpRequest")
	}
	resp, e := c.Do(r)
	if e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(e, context.DeadlineExceeded) {
			return nil, context.DeadlineExceeded
		}
		return nil, contract.ErrUpstream
	}
	defer resp.Body.Close()
	if resp.Request != nil && resp.Request.URL != nil && strings.Contains(resp.Request.URL.Path, "antispiderShowVerify") {
		return nil, contract.ErrVerificationRequired
	}
	if resp.StatusCode == 429 {
		return nil, contract.ErrRateLimited
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 || (resp.StatusCode >= 300 && resp.StatusCode < 400) {
		return nil, contract.ErrAuthRequired
	}
	if resp.StatusCode != 200 {
		return nil, contract.ErrUpstream
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, contract.ErrUpstream
	}
	if len(b) > maxBody {
		return nil, contract.ErrParse
	}
	if verificationPage(b) {
		return nil, contract.ErrVerificationRequired
	}
	return b, nil
}
func checked(b []byte) error {
	s := string(b)
	for _, v := range []string{"没有此页面访问权限", "参数传递不正确"} {
		if strings.Contains(s, v) {
			return contract.ErrParse
		}
	}
	for _, v := range []string{"请先登录", "请重新登录", "id=\"loginForm\"", "id=\"phoneLogin\"", "/fanyalogin"} {
		if strings.Contains(s, v) {
			return contract.ErrAuthRequired
		}
	}
	return nil
}
func (a *adapter) BeginAuth(ctx context.Context, scope contract.Scope) (contract.AuthResult, error) {
	s, c, j, e := a.restore(scope, nil)
	if e != nil {
		return contract.AuthResult{}, e
	}
	b, e := request(ctx, c, http.MethodGet, login, nil)
	if e != nil {
		return contract.AuthResult{}, e
	}
	inputs, e := hidden(b)
	if e != nil {
		return contract.AuthResult{}, e
	}
	s.UUID, s.Enc = inputs["uuid"], inputs["enc"]
	if s.UUID == "" || s.Enc == "" {
		return contract.AuthResult{}, contract.ErrParse
	}
	s.DoubleFactor = inputs["doubleFactorLogin"]
	if s.DoubleFactor == "" {
		s.DoubleFactor = "0"
	}
	s.ForbidOther = inputs["forbidotherlogin"]
	if s.ForbidOther == "" {
		s.ForbidOther = "0"
	}
	qr, e := request(ctx, c, http.MethodGet, passport+"/createqr?"+url.Values{"uuid": {s.UUID}, "fid": {"-1"}}.Encode(), nil)
	if e != nil {
		return contract.AuthResult{}, e
	}
	media := "image/png"
	if !bytes.HasPrefix(qr, []byte("\x89PNG\r\n\x1a\n")) {
		if bytes.HasPrefix(qr, []byte{0xff, 0xd8, 0xff}) {
			media = "image/jpeg"
		} else {
			return contract.AuthResult{}, contract.ErrParse
		}
	}
	// The official page stops its UI after 50 polls, but research/live/qr_login.py
	// successfully polls for up to 100 * 3s (including a 3.5-minute login).
	// This is a local polling budget; the server still decides QR validity.
	s.Expires = time.Now().UTC().Add(300 * time.Second)
	return contract.AuthResult{Status: "pending", QRImage: qr, QRMediaType: media, ExpiresAt: s.Expires, PollAfter: 3 * time.Second, Session: save(s, j)}, nil
}

// qrStatusType accepts the numeric protocol documented in repository research
// and the quoted decimal codes returned by the current anonymous login endpoint.
// Keep other JSON types and noncanonical numeric strings invalid.
type qrStatusType int

func (v *qrStatusType) UnmarshalJSON(b []byte) error {
	var code int
	if len(b) > 0 && b[0] == '"' {
		var s string
		if json.Unmarshal(b, &s) != nil {
			return contract.ErrParse
		}
		n, err := strconv.Atoi(s)
		if err != nil || strconv.Itoa(n) != s {
			return contract.ErrParse
		}
		code = n
	} else if bytes.Equal(b, []byte("null")) || json.Unmarshal(b, &code) != nil {
		return contract.ErrParse
	}
	*v = qrStatusType(code)
	return nil
}

func (a *adapter) PollAuth(ctx context.Context, scope contract.Scope, session contract.Session) (contract.AuthResult, error) {
	s, c, j, e := a.restore(scope, session)
	if e != nil {
		return contract.AuthResult{}, e
	}
	if e = ctx.Err(); e != nil {
		return contract.AuthResult{}, e
	}
	if s.Authorized {
		return contract.AuthResult{Status: "authorized", Session: save(s, j)}, nil
	}
	if s.UUID == "" || s.Enc == "" {
		return contract.AuthResult{}, contract.ErrAuthRequired
	}
	if time.Now().After(s.Expires) {
		return contract.AuthResult{Status: "expired", ExpiresAt: s.Expires, Session: save(s, j)}, nil
	}
	b, e := request(ctx, c, http.MethodPost, passport+"/getauthstatus/v2", url.Values{"uuid": {s.UUID}, "enc": {s.Enc}, "doubleFactorLogin": {s.DoubleFactor}, "forbidotherlogin": {s.ForbidOther}})
	if e != nil {
		return contract.AuthResult{}, e
	}
	type statusFields struct {
		Status    *bool         `json:"status"`
		Type      *qrStatusType `json:"type"`
		TwoFactor bool          `json:"containTwoFactorLogin"`
	}
	var v struct {
		statusFields
		Data *statusFields `json:"data"`
	}
	if json.Unmarshal(b, &v) != nil || (v.Status == nil && (v.Data == nil || v.Data.Status == nil)) {
		return contract.AuthResult{}, contract.ErrParse
	}
	success := v.Status != nil && *v.Status
	twoFactor := v.TwoFactor
	code := v.Type
	if v.Data != nil {
		success = success || (v.Data.Status != nil && *v.Data.Status)
		twoFactor = twoFactor || v.Data.TwoFactor
		if v.Data.Type != nil {
			code = v.Data.Type
		}
	}
	status := "pending"
	if success {
		if twoFactor {
			return contract.AuthResult{}, contract.ErrUnsupported
		}
		// The auth response has already populated this private jar. Research's
		// successful flow persists it directly; /base is an optional profile
		// navigation and must not discard a completed login on network failure.
		if !hasIdentity(j) {
			return contract.AuthResult{}, contract.ErrAuthRequired
		}
		s.Authorized = true
		s.UUID = ""
		s.Enc = ""
		status = "authorized"
	} else if code != nil {
		switch *code {
		case 4:
			status = "scanned"
		case 6:
			status = "expired"
			s.Expires = time.Now().UTC()
		case 7:
			return contract.AuthResult{}, contract.ErrAuthRequired
		case 0, 1, 2, 3:
		default:
			return contract.AuthResult{}, contract.ErrParse
		}
	} else {
		return contract.AuthResult{}, contract.ErrParse
	}
	return contract.AuthResult{Status: status, ExpiresAt: s.Expires, PollAfter: 3 * time.Second, Session: save(s, j)}, nil
}
func hasIdentity(j *jar) bool {
	for _, origin := range []string{passport, api, "https://i.chaoxing.com"} {
		u, _ := url.Parse(origin)
		identity, authenticated := false, false
		for _, c := range j.Cookies(u) {
			if strings.TrimSpace(c.Value) == "" {
				continue
			}
			switch c.Name {
			case "_uid", "UID":
				identity = true
			case "cx_p_token", "p_auth_token", "vc3", "_d":
				authenticated = true
			}
		}
		if identity && authenticated {
			return true
		}
	}
	return false
}
