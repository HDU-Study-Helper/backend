// Package jwxt implements the researched HDU SSO and read-only academic APIs.
package jwxt

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"academic/backend/internal/contract"
	"golang.org/x/net/publicsuffix"
)

const (
	base     = "https://newjw.hdu.edu.cn/jwglxt/"
	menu     = base + "xtgl/index_initMenu.html?jsdm=xs&echarts=1"
	loginURL = "https://sso.hdu.edu.cn/login?service=http%3A%2F%2Fnewjw.hdu.edu.cn%2Fsso%2Fdriot4login"
	maxBody  = 4 << 20
)

type adapter struct{ client http.Client }

func New(deps contract.Dependencies) (contract.Adapter, error) {
	c := http.Client{}
	if deps.HTTPClient != nil {
		c = *deps.HTTPClient
	}
	c.Jar = nil
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if c.Timeout <= 0 || c.Timeout > 30*time.Second {
		c.Timeout = 30 * time.Second
	}
	return &adapter{client: c}, nil
}

func (*adapter) Describe() contract.Descriptor {
	return contract.Descriptor{Source: "jwxt", Name: "杭电教务", PasswordLogin: true, ManualSyncOnly: true,
		Resources:   []contract.Resource{contract.Schedule, contract.Course},
		Limitations: []string{"Offline fixtures only; current Go login and data APIs have not been live tested", "Manual sync only; academic starting year and term 1/2 required", "No automatic login, credential refresh, course selection or withdrawal; password is not retained", "Schedule keeps raw weeks and periods; practice records are separate observations"}}
}
func (*adapter) BeginAuth(context.Context, contract.Scope) (contract.AuthResult, error) {
	return contract.AuthResult{}, contract.ErrUnsupported
}
func (*adapter) PollAuth(context.Context, contract.Scope, contract.Session) (contract.AuthResult, error) {
	return contract.AuthResult{}, contract.ErrUnsupported
}

type cookieEntry struct {
	Origin string
	Cookie http.Cookie
}
type sessionState struct {
	Version  int
	Scope    contract.Scope
	Username string
	Cookies  []cookieEntry
}
type privateJar struct {
	http.CookieJar
	entries []cookieEntry
}

func (j *privateJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	j.CookieJar.SetCookies(u, cookies)
	for _, cookie := range cookies {
		c := *cookie
		if c.MaxAge > 0 {
			c.Expires = time.Now().Add(time.Duration(c.MaxAge) * time.Second)
			c.MaxAge = 0
		}
		if c.Path == "" {
			i := strings.LastIndex(u.Path, "/")
			c.Path = "/"
			if i > 0 {
				c.Path = u.Path[:i]
			}
		}
		origin := u.Scheme + "://" + u.Host + "/"
		found := false
		for i, e := range j.entries {
			if e.Origin == origin && e.Cookie.Name == c.Name && e.Cookie.Domain == c.Domain && e.Cookie.Path == c.Path {
				j.entries[i] = cookieEntry{origin, c}
				found = true
				break
			}
		}
		if !found {
			j.entries = append(j.entries, cookieEntry{origin, c})
		}
	}
}
func (a *adapter) clientFor(scope contract.Scope, raw contract.Session) (*http.Client, *privateJar, sessionState, error) {
	s := sessionState{Version: 1, Scope: scope}
	if scope.UserID == "" || scope.ConnectionID == "" {
		return nil, nil, s, contract.ErrInvalid
	}
	if len(raw) > 0 && (len(raw) > 2<<20 || json.Unmarshal(raw, &s) != nil || s.Version != 1 || s.Scope != scope || len(s.Cookies) > 256) {
		return nil, nil, s, contract.ErrAuthRequired
	}
	b, _ := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	j := &privateJar{CookieJar: b}
	for _, e := range s.Cookies {
		u, err := url.Parse(e.Origin)
		if err != nil || !allowed(u) {
			return nil, nil, s, contract.ErrAuthRequired
		}
		c := e.Cookie
		if c.MaxAge < 0 || (!c.Expires.IsZero() && !time.Now().Before(c.Expires)) {
			continue
		}
		if c.MaxAge > 0 {
			return nil, nil, s, contract.ErrAuthRequired
		}
		j.SetCookies(u, []*http.Cookie{&c})
	}
	c := a.client
	c.Jar = j
	return &c, j, s, nil
}
func saveSession(s sessionState, j *privateJar) contract.Session {
	s.Cookies = j.entries
	b, _ := json.Marshal(s)
	return b
}
func allowed(u *url.URL) bool {
	return u != nil && u.Scheme == "https" && u.User == nil && (u.Port() == "" || u.Port() == "443") && (u.Hostname() == "sso.hdu.edu.cn" || u.Hostname() == "newjw.hdu.edu.cn")
}

// Upgrade only the callback paths observed by the historical research. Never
// change CAS's registered service value or forward a password through redirects.
func redirectTarget(from *url.URL, location string) (*url.URL, error) {
	u, e := from.Parse(location)
	if e != nil || location == "" {
		return nil, contract.ErrParse
	}
	if u.Scheme == "http" && u.Hostname() == "newjw.hdu.edu.cn" && u.User == nil && u.Port() == "" {
		p := strings.SplitN(u.Path, ";", 2)[0]
		if p == "/sso/driot4login" || p == "/jwglxt/ticketlogin" || p == "/jwglxt/xtgl/index_initMenu.html" {
			u.Scheme = "https"
		}
	}
	if !allowed(u) {
		return nil, contract.ErrUpstream
	}
	return u, nil
}
func request(ctx context.Context, c *http.Client, method, target string, form url.Values, follow bool) ([]byte, *url.URL, error) {
	u, e := url.Parse(target)
	if e != nil || !allowed(u) {
		return nil, nil, contract.ErrInvalid
	}
	for redirects := 0; redirects <= 8; redirects++ {
		if e := ctx.Err(); e != nil {
			return nil, nil, e
		}
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		r, e := http.NewRequestWithContext(ctx, method, u.String(), body)
		if e != nil {
			return nil, nil, contract.ErrInvalid
		}
		r.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
		r.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
		r.Header.Set("Referer", menu)
		if u.Hostname() == "sso.hdu.edu.cn" {
			r.Header.Set("Referer", loginURL)
		}
		if form != nil {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if u.Hostname() == "newjw.hdu.edu.cn" {
				r.Header.Set("X-Requested-With", "XMLHttpRequest")
			}
		}
		resp, e := c.Do(r)
		if e != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			if errors.Is(e, context.DeadlineExceeded) {
				return nil, nil, context.DeadlineExceeded
			}
			return nil, nil, contract.ErrUpstream
		}
		status := resp.StatusCode
		if status >= 300 && status < 400 {
			location := resp.Header.Get("Location")
			resp.Body.Close()
			if !follow {
				return nil, nil, contract.ErrAuthRequired
			}
			if status != 301 && status != 302 && status != 303 && status != 307 && status != 308 {
				return nil, nil, contract.ErrUpstream
			}
			if method == http.MethodPost && (status == 307 || status == 308) {
				return nil, nil, contract.ErrUpstream
			}
			u, e = redirectTarget(u, location)
			if e != nil {
				return nil, nil, e
			}
			method = http.MethodGet
			form = nil
			continue
		}
		if status != 200 {
			resp.Body.Close()
			switch status {
			case 401, 403:
				return nil, nil, contract.ErrAuthRequired
			case 429:
				return nil, nil, contract.ErrRateLimited
			default:
				return nil, nil, contract.ErrUpstream
			}
		}
		b, e := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
		resp.Body.Close()
		if e != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			return nil, nil, contract.ErrUpstream
		}
		if len(b) > maxBody {
			return nil, nil, contract.ErrParse
		}
		return b, u, nil
	}
	return nil, nil, contract.ErrUpstream
}

var _ contract.Adapter = (*adapter)(nil)
var _ contract.PasswordLogin = (*adapter)(nil)
var _ contract.AcademicTermReader = (*adapter)(nil)
