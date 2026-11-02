// Package ucampus implements bounded, read-only U Campus AI observations.
package ucampus

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"academic/backend/internal/contract"
)

const portal = "https://uai.unipus.cn"
const content = "https://ucontent.unipus.cn"
const maxBody = 4 << 20
const maxItems = 500

type adapter struct {
	client  http.Client
	mu      sync.Mutex
	sockets map[string]*authSocket
}

var _ contract.Adapter = (*adapter)(nil)

func New(deps contract.Dependencies) (contract.Adapter, error) {
	c := http.Client{Timeout: 30 * time.Second}
	if deps.HTTPClient != nil {
		c = *deps.HTTPClient
		if c.Timeout == 0 {
			c.Timeout = 30 * time.Second
		}
	}
	c.Jar = nil
	// Redirects may cross a credential boundary or lead to a login HTML page.
	c.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &adapter{client: c, sockets: make(map[string]*authSocket)}, nil
}
func (*adapter) Describe() contract.Descriptor {
	return contract.Descriptor{Source: "ucampus", Name: "U校园 AI", QRLogin: true, Resources: []contract.Resource{contract.Course, contract.Assignment, contract.Progress}, Limitations: []string{"官方APP二维码与WebSocket状态；成功扫码/票据交换未使用真实账号验证，重启或取消需重新扫码", "QR会话仅授权门户课程；assignment/progress需额外受控导入官方annotator会话，不生成签名token；assignment仅教材任务，不含教师作业", "考试、通知未支持；无增量，完成后下轮全扫", "接口经源码核对与Mock测试，未使用真实账号验证"}}
}

// SessionInput is sensitive. Only a trusted host credential-import path may use it;
// never accept it from a model, ordinary metadata or an unauthenticated route.
// Tokens must come from the user's legitimately authorized session. No token is minted here.
type SessionInput struct {
	Authorization  string         `json:"authorization"`
	OpenID         string         `json:"open_id"`
	Annotator      string         `json:"annotator"`
	PortalCookies  []*http.Cookie `json:"portal_cookies"`
	ContentCookies []*http.Cookie `json:"content_cookies"`
	ExpiresAt      time.Time      `json:"expires_at"`
}
type state struct {
	Version   int            `json:"v"`
	Scope     contract.Scope `json:"scope"`
	AccountID string         `json:"account_id"`
	Challenge *qrState       `json:"challenge,omitempty"`
	SessionInput
}

// ImportSession verifies the supplied authorized session against the official account endpoint.
func ImportSession(ctx context.Context, scope contract.Scope, raw []byte) (contract.Session, error) {
	return ImportSessionWithDependencies(ctx, scope, raw, contract.Dependencies{})
}

// ImportSessionWithDependencies permits the host to inject its controlled HTTP transport.
func ImportSessionWithDependencies(ctx context.Context, scope contract.Scope, raw []byte, deps contract.Dependencies) (contract.Session, error) {
	if err := validate(ctx, scope); err != nil {
		return nil, err
	}
	var input SessionInput
	if len(raw) > 128<<10 || json.Unmarshal(raw, &input) != nil {
		return nil, contract.ErrInvalid
	}
	s := state{Version: 1, Scope: scope, SessionInput: input}
	if err := checkState(s); err != nil {
		return nil, err
	}
	ad, _ := New(deps)
	a := ad.(*adapter)
	client := a.client
	jar := newTrackedJar()
	client.Jar = jar
	for origin, cookies := range map[string][]*http.Cookie{portal: s.PortalCookies, content: s.ContentCookies} {
		u, _ := url.Parse(origin)
		jar.SetCookies(u, cookies)
	}
	var root map[string]json.RawMessage
	if err := a.get(ctx, &client, s, portal+"/api/account/user/info", true, &root); err != nil {
		return nil, err
	}
	var value struct {
		UserInfo map[string]json.RawMessage `json:"userInfo"`
	}
	if json.Unmarshal(root["value"], &value) != nil {
		return nil, contract.ErrParse
	}
	s.AccountID = text(value.UserInfo["appUserId"])
	if s.AccountID == "" {
		return nil, contract.ErrParse
	}
	if id := text(value.UserInfo["ssoId"]); input.OpenID != "" && id != "" && input.OpenID != id {
		return nil, contract.ErrAuthRequired
	}
	s.PortalCookies = jar.snapshot(portal)
	s.ContentCookies = jar.snapshot(content)
	b, _ := json.Marshal(s)
	return b, nil
}
func newSession(scope contract.Scope, input SessionInput) (contract.Session, error) {
	if scope.UserID == "" || scope.ConnectionID == "" {
		return nil, contract.ErrInvalid
	}
	s := state{Version: 1, Scope: scope, SessionInput: input}
	if err := checkState(s); err != nil {
		return nil, err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return nil, contract.ErrInvalid
	}
	return b, nil
}
func checkState(s state) error {
	if s.Version != 1 || (s.Authorization == "" && len(s.PortalCookies) == 0) {
		return contract.ErrAuthRequired
	}
	for _, h := range []string{s.Authorization, s.OpenID, s.Annotator} {
		if strings.ContainsAny(h, "\r\n") || len(h) > 32768 {
			return contract.ErrInvalid
		}
	}
	for _, cs := range [][]*http.Cookie{s.PortalCookies, s.ContentCookies} {
		if len(cs) > 100 {
			return contract.ErrInvalid
		}
		for _, c := range cs {
			if c == nil || c.Valid() != nil {
				return contract.ErrInvalid
			}
		}
	}
	if !s.ExpiresAt.IsZero() && !time.Now().Before(s.ExpiresAt) {
		return contract.ErrAuthRequired
	}
	return nil
}
func validate(ctx context.Context, s contract.Scope) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.UserID == "" || s.ConnectionID == "" {
		return contract.ErrInvalid
	}
	return nil
}
func restore(raw contract.Session, scope contract.Scope) (state, error) {
	var s state
	if len(raw) > 128<<10 || json.Unmarshal(raw, &s) != nil {
		return s, contract.ErrAuthRequired
	}
	if s.Scope != scope {
		return s, contract.ErrAuthRequired
	}
	return s, checkState(s)
}

type course struct {
	ID        string `json:"id"`
	Class     string `json:"class"`
	Instance  string `json:"instance"`
	Resource  string `json:"resource"`
	Name      string `json:"name"`
	ClassName string `json:"class_name"`
	Start     any    `json:"start,omitempty"`
	End       any    `json:"end,omitempty"`
}
type cursorState struct {
	Version  int               `json:"v"`
	Scope    contract.Scope    `json:"scope"`
	Resource contract.Resource `json:"resource"`
	Binding  string            `json:"binding"`
	Courses  []course          `json:"courses"`
	Units    []string          `json:"units"`
	Loaded   bool              `json:"loaded"`
}

func binding(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
func compound(ids ...string) string {
	b, _ := json.Marshal(ids)
	return base64.RawURLEncoding.EncodeToString(b)
}
func (a *adapter) Sync(ctx context.Context, scope contract.Scope, raw contract.Session, resource contract.Resource, cursor string) (contract.Page, error) {
	if err := validate(ctx, scope); err != nil {
		return contract.Page{}, err
	}
	if resource != contract.Course && resource != contract.Assignment && resource != contract.Progress {
		return contract.Page{}, contract.ErrUnsupported
	}
	s, err := restore(raw, scope)
	if err != nil {
		return contract.Page{}, err
	}
	c := cursorState{Version: 1, Scope: scope, Resource: resource, Binding: binding(raw)}
	if cursor != "" {
		b, e := base64.RawURLEncoding.DecodeString(cursor)
		if e != nil || len(b) > 1<<20 || json.Unmarshal(b, &c) != nil || c.Version != 1 || c.Scope != scope || c.Resource != resource || c.Binding != binding(raw) || len(c.Courses) > maxItems || len(c.Units) > maxItems || len(c.Courses) == 0 {
			return contract.Page{}, contract.ErrInvalid
		}
	}
	jar := newTrackedJar()
	for origin, cookies := range map[string][]*http.Cookie{portal: s.PortalCookies, content: s.ContentCookies} {
		u, _ := url.Parse(origin)
		jar.SetCookies(u, cookies)
	}
	client := a.client
	client.Jar = jar
	if cursor == "" {
		c.Courses, err = a.courses(ctx, &client, s)
		if err != nil {
			return contract.Page{}, err
		}
	}
	out := contract.Page{Records: []contract.Record{}}
	if resource == contract.Course {
		for _, co := range c.Courses {
			out.Records = append(out.Records, contract.Record{Kind: resource, ExternalID: compound(co.ID, co.Class, co.Instance, co.Resource), ParentID: compound(co.ID, co.Class), Title: co.Name, Fields: map[string]any{"course_id": co.ID, "class_id": co.Class, "instance_id": co.Instance, "course_resource_id": co.Resource, "class_name": co.ClassName, "start_time_raw": co.Start, "end_time_raw": co.End}, ObservedAt: time.Now().UTC()})
		}
		c.Courses = nil
	} else {
		if s.OpenID == "" || s.Annotator == "" {
			return contract.Page{}, fmt.Errorf("%w: textbook progress requires separately imported official annotator session; QR currently authorizes portal courses only", contract.ErrUnsupported)
		}
		// At most one course discovery and one unit read per call, including empty units.
		if len(c.Courses) > 0 {
			co := c.Courses[0]
			if !c.Loaded {
				var root map[string]json.RawMessage
				err = a.get(ctx, &client, s, content+"/course/api/v2/course_progress/"+url.PathEscape(co.Instance)+"/"+url.PathEscape(s.OpenID)+"/default", false, &root)
				if err != nil {
					return contract.Page{}, err
				}
				var rt struct {
					Units map[string]json.RawMessage `json:"units"`
				}
				if json.Unmarshal(root["rt"], &rt) != nil || rt.Units == nil || len(rt.Units) > maxItems {
					return contract.Page{}, contract.ErrParse
				}
				for id := range rt.Units {
					if !validID(id) {
						return contract.Page{}, contract.ErrParse
					}
					c.Units = append(c.Units, id)
				}
				sort.Strings(c.Units)
				c.Loaded = true
			}
			if len(c.Units) > 0 {
				out.Records, err = a.tasks(ctx, &client, s, co, c.Units[0], resource)
				if err != nil {
					return contract.Page{}, err
				}
				c.Units = c.Units[1:]
			}
			if len(c.Units) == 0 {
				c.Courses = c.Courses[1:]
				c.Loaded = false
			}
		}
	}
	out.HasMore = len(c.Courses) > 0
	if out.HasMore {
		b, _ := json.Marshal(c)
		out.NextCursor = base64.RawURLEncoding.EncodeToString(b)
	}
	// Preserve fresh cookies only in sensitive Session. Serialize host-only cookies per origin.
	for origin, target := range map[string]*[]*http.Cookie{portal: &s.PortalCookies, content: &s.ContentCookies} {
		*target = jar.snapshot(origin)
	}
	out.Session, _ = json.Marshal(s)
	// Cursor binding follows the returned session, including any refreshed cookies.
	if out.HasMore {
		c.Binding = binding(out.Session)
		b, _ := json.Marshal(c)
		out.NextCursor = base64.RawURLEncoding.EncodeToString(b)
	}
	return out, nil
}
func validID(s string) bool {
	return s != "" && len(s) < 512 && !strings.ContainsAny(s, "/?#\\\r\n") && s != "." && s != ".."
}
func scalar(raw json.RawMessage) any {
	var v any
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.UseNumber()
	if d.Decode(&v) != nil {
		return nil
	}
	switch v.(type) {
	case string, json.Number, bool:
		return v
	}
	return nil
}
func text(raw json.RawMessage) string {
	v := scalar(raw)
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return string(x)
	}
	return ""
}
func (a *adapter) courses(ctx context.Context, client *http.Client, s state) ([]course, error) {
	var root map[string]json.RawMessage
	if err := a.get(ctx, client, s, portal+"/api/cmgt/course/getHomeCourseListByStudent", true, &root); err != nil {
		return nil, err
	}
	var value struct {
		Courses []map[string]json.RawMessage `json:"courseList"`
	}
	if json.Unmarshal(root["value"], &value) != nil || value.Courses == nil || len(value.Courses) > maxItems {
		return nil, contract.ErrParse
	}
	out := []course{}
	for _, row := range value.Courses {
		var resources []map[string]json.RawMessage
		if json.Unmarshal(row["courseResourceList"], &resources) != nil || resources == nil {
			return nil, contract.ErrParse
		}
		for _, r := range resources {
			co := course{ID: text(row["id"]), Class: text(row["classId"]), ClassName: text(row["className"]), Instance: text(r["instanceId"]), Resource: text(r["id"]), Name: text(r["name"]), Start: scalar(row["startTime"]), End: scalar(row["endTime"])}
			if co.Name == "" {
				co.Name = text(row["name"])
			}
			if !validID(co.ID) || !validID(co.Instance) || len(out) >= maxItems {
				return nil, contract.ErrParse
			}
			out = append(out, co)
		}
	}
	return out, nil
}
func (a *adapter) tasks(ctx context.Context, client *http.Client, s state, co course, unit string, kind contract.Resource) ([]contract.Record, error) {
	var root map[string]json.RawMessage
	path := content + "/course/api/v2/course_progress/" + url.PathEscape(co.Instance) + "/" + url.PathEscape(unit) + "/" + url.PathEscape(s.OpenID) + "/default/"
	if err := a.get(ctx, client, s, path, false, &root); err != nil {
		return nil, err
	}
	var rt struct {
		Leafs   map[string]json.RawMessage `json:"leafs"`
		Version json.RawMessage            `json:"publish_version"`
	}
	if json.Unmarshal(root["rt"], &rt) != nil || rt.Leafs == nil || len(rt.Leafs) > maxItems {
		return nil, contract.ErrParse
	}
	ids := make([]string, 0, len(rt.Leafs))
	for id := range rt.Leafs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := []contract.Record{}
	for _, id := range ids {
		if !validID(id) {
			return nil, contract.ErrParse
		}
		var leaf map[string]json.RawMessage
		if json.Unmarshal(rt.Leafs[id], &leaf) != nil || leaf == nil {
			return nil, contract.ErrParse
		}
		fields := map[string]any{"course_id": co.ID, "class_id": co.Class, "instance_id": co.Instance, "course_resource_id": co.Resource, "unit_id": unit, "task_id": id, "task_type": "textbook_task"}
		for _, key := range []string{"duration", "tab_type"} {
			if v := scalar(leaf[key]); v != nil {
				fields[key] = v
			}
		}
		for key, allowed := range map[string][]string{"state": {"pass", "pass2", "perm"}, "strategies": {"start_time", "end_time", "required", "min_score_pct", "statistic_mode_out"}} {
			if len(leaf[key]) == 0 || string(leaf[key]) == "null" {
				continue
			}
			var nested map[string]json.RawMessage
			if json.Unmarshal(leaf[key], &nested) != nil || nested == nil {
				return nil, contract.ErrParse
			}
			safe := map[string]any{}
			for _, k := range allowed {
				if v := scalar(nested[k]); v != nil {
					safe[k] = v
				}
			}
			fields[key] = safe
		}
		out = append(out, contract.Record{Kind: kind, ExternalID: compound(co.ID, co.Class, co.Instance, co.Resource, unit, id), ParentID: compound(co.ID, co.Class, co.Instance, co.Resource, unit), Fields: fields, SourceVersion: text(rt.Version), ObservedAt: time.Now().UTC()})
	}
	return out, nil
}
func (*adapter) get(ctx context.Context, client *http.Client, s state, endpoint string, portalAPI bool, out *map[string]json.RawMessage) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return contract.ErrInvalid
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", s.Authorization)
	req.Header.Set("u-openid", s.OpenID)
	req.Header.Set("x-csrftoken", s.OpenID)
	if !portalAPI {
		req.Header.Set("x-annotator-auth-token", s.Annotator)
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return contract.ErrUpstream
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode >= 300 && resp.StatusCode < 400:
		return contract.ErrAuthRequired
	case resp.StatusCode == 429:
		return contract.ErrRateLimited
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return contract.ErrUpstream
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return contract.ErrUpstream
	}
	if len(b) > maxBody {
		return contract.ErrParse
	}
	if json.Unmarshal(b, out) != nil || *out == nil {
		return contract.ErrParse
	}
	code := text((*out)["code"])
	if code == "401" || code == "403" {
		return contract.ErrAuthRequired
	}
	if code == "429" {
		return contract.ErrRateLimited
	}
	expected := "0"
	if portalAPI {
		expected = "1"
	}
	if code != expected {
		return errors.Join(contract.ErrParse, fmt.Errorf("unexpected platform success code"))
	}
	return nil
}
