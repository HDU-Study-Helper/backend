// Package mooc implements read-only China University MOOC adapters.
package mooc

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"academic/backend/internal/contract"
)

const origin = "https://www.icourse163.org"
const maxBody = 4 << 20

type adapter struct{ client http.Client }

var _ contract.Adapter = (*adapter)(nil)

func New(deps contract.Dependencies) (contract.Adapter, error) {
	c := http.Client{}
	if deps.HTTPClient != nil {
		c = *deps.HTTPClient
	}
	c.Jar = nil
	c.Timeout = 20 * time.Second
	// Cookie-exchange redirects must stay on the official HTTPS origin. Never
	// delegate redirects to injected callbacks that could leak credentials.
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 6 || req.URL.Scheme != "https" || req.URL.Host != "www.icourse163.org" {
			return contract.ErrUpstream
		}
		return req.Context().Err()
	}
	return &adapter{client: c}, nil
}
func (a *adapter) Describe() contract.Descriptor {
	return contract.Descriptor{
		Source: "mooc", Name: "中国大学 MOOC / SPOC", QRLogin: true,
		Resources:   []contract.Resource{contract.Course, contract.Assignment, contract.Exam, contract.Progress},
		Limitations: []string{"全量轮询，父层按内容哈希幂等；没有删除或增量 API", "作业和考试仅目录元数据，不开卷、不取答案、不提交", "进度仅目录显式个人状态与 lastLearnUnitId，不代表完整完成率", "SPOC 空目录不保证覆盖源课程；公告尚无核实端点", "本适配器仅通过 Mock 验证，未使用真实账号验收"},
	}
}

type savedCookie struct {
	URL    string
	Cookie http.Cookie
}
type state struct {
	Version    int
	Scope      contract.Scope
	Cookies    []savedCookie
	PollKey    string
	QRURL      string
	Expires    time.Time
	Authorized bool
}
type privateJar struct {
	http.CookieJar
	saved []savedCookie
}

func (j *privateJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	j.CookieJar.SetCookies(u, cookies)
	for _, v := range cookies {
		c := *v
		if c.MaxAge > 0 {
			c.Expires = time.Now().Add(time.Duration(c.MaxAge) * time.Second)
			c.MaxAge = 0
		}
		if c.Domain == "" {
			c.Domain = u.Hostname()
		}
		if c.Path == "" {
			c.Path = u.Path
			i := strings.LastIndex(c.Path, "/")
			if i <= 0 {
				c.Path = "/"
			} else {
				c.Path = c.Path[:i]
			}
		}
		found := false
		for i, x := range j.saved {
			if x.Cookie.Name == c.Name && x.Cookie.Domain == c.Domain && x.Cookie.Path == c.Path {
				j.saved[i] = savedCookie{u.String(), c}
				found = true
				break
			}
		}
		if !found {
			j.saved = append(j.saved, savedCookie{u.String(), c})
		}
	}
}
func validScope(s contract.Scope) bool {
	return s.UserID != "" && s.ConnectionID != "" && len(s.UserID) < 256 && len(s.ConnectionID) < 256
}
func (a *adapter) restore(scope contract.Scope, session contract.Session) (*state, *http.Client, error) {
	if !validScope(scope) {
		return nil, nil, contract.ErrInvalid
	}
	s := &state{Version: 1, Scope: scope}
	if len(session) > 0 {
		if len(session) > 256<<10 || json.Unmarshal(session, s) != nil || s.Version != 1 || s.Scope != scope || len(s.Cookies) > 256 {
			return nil, nil, contract.ErrInvalid
		}
	}
	jar, _ := cookiejar.New(nil)
	j := &privateJar{CookieJar: jar}
	for _, x := range s.Cookies {
		u, e := url.Parse(x.URL)
		if e != nil || u.Scheme != "https" || u.Host != "www.icourse163.org" {
			return nil, nil, contract.ErrInvalid
		}
		j.SetCookies(u, []*http.Cookie{&x.Cookie})
	}
	c := a.client
	c.Jar = j
	return s, &c, nil
}
func save(s *state, c *http.Client) (contract.Session, error) {
	s.Cookies = c.Jar.(*privateJar).saved
	if len(s.Cookies) > 256 {
		return nil, contract.ErrParse
	}
	b, e := json.Marshal(s)
	if e != nil || len(b) > 256<<10 {
		return nil, contract.ErrParse
	}
	return b, nil
}
func request(ctx context.Context, c *http.Client, method, path, referer string, form url.Values) ([]byte, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, e := http.NewRequestWithContext(ctx, method, origin+path, body)
	if e != nil {
		return nil, contract.ErrInvalid
	}
	req.Header.Set("Referer", referer)
	req.Header.Set("Origin", origin)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=UTF-8")
	}
	u, _ := url.Parse(origin)
	for _, cookie := range c.Jar.Cookies(u) {
		if cookie.Name == "NTESSTUDYSI" {
			req.Header.Set("edu-script-token", cookie.Value)
		}
	}
	res, e := c.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, contract.ErrUpstream
	}
	defer res.Body.Close()
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return nil, contract.ErrAuthRequired
	}
	if res.StatusCode == 429 {
		return nil, contract.ErrRateLimited
	}
	if res.StatusCode != 200 {
		return nil, contract.ErrUpstream
	}
	b, e := io.ReadAll(io.LimitReader(res.Body, maxBody+1))
	if e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, contract.ErrUpstream
	}
	if len(b) > maxBody {
		return nil, contract.ErrParse
	}
	return b, nil
}
func decode(b []byte) (map[string]any, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var env map[string]any
	if d.Decode(&env) != nil {
		return nil, contract.ErrParse
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return nil, contract.ErrParse
	}
	code, ok := env["code"].(json.Number)
	if !ok {
		return nil, contract.ErrParse
	}
	if code.String() == "1" || code.String() == "-1007" {
		return nil, contract.ErrAuthRequired
	}
	if code.String() != "0" {
		return nil, contract.ErrUpstream
	}
	result, ok := env["result"].(map[string]any)
	if !ok {
		return nil, contract.ErrParse
	}
	return result, nil
}
func rpc(ctx context.Context, c *http.Client, method, referer string, form url.Values) (map[string]any, error) {
	u, _ := url.Parse(origin)
	csrf := ""
	for _, cookie := range c.Jar.Cookies(u) {
		if cookie.Name == "NTESSTUDYSI" {
			csrf = cookie.Value
		}
	}
	if csrf == "" {
		return nil, contract.ErrAuthRequired
	}
	b, e := request(ctx, c, "POST", "/web/j/"+method+".rpc?csrfKey="+url.QueryEscape(csrf), referer, form)
	if e != nil {
		return nil, e
	}
	return decode(b)
}
func text(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	}
	return ""
}
func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func first(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			return v
		}
	}
	return nil
}
