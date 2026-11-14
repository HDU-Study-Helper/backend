package chaoxing

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"academic/backend/internal/contract"
	"golang.org/x/net/html"
)

const refreshHome = "https://i.chaoxing.com"

var _ contract.SessionRefresher = (*adapter)(nil)

// RefreshSession revisits the official existing-login entry points, collecting
// server-issued cookies, then verifies access to the existing course endpoint.
// It cannot renew an expired root login, run login JavaScript, or create a new
// QR/password challenge. A successful check does not promise a longer lifetime.
func (a *adapter) RefreshSession(ctx context.Context, scope contract.Scope, session contract.Session) (contract.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s, c, j, err := a.restore(scope, session)
	if err != nil {
		return nil, err
	}
	if !s.Authorized {
		return nil, contract.ErrAuthRequired
	}
	identity, err := refreshIdentity(s.Cookies)
	if err != nil {
		return nil, err
	}
	// These are exact known read-only entry pages. Do not use official(), which
	// also admits arbitrary public Chaoxing subdomains and unrelated GET routes.
	c.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if sameRefreshIdentity(identity, j.Entries) != nil {
			return http.ErrUseLastResponse
		}
		if len(via) >= 4 || r.Method != http.MethodGet || !refreshEntry(r.URL) {
			return http.ErrUseLastResponse
		}
		return nil
	}
	for _, target := range []string{refreshHome, login} {
		_, err = request(ctx, c, http.MethodGet, target, nil)
		if err != nil && !errors.Is(err, contract.ErrAuthRequired) {
			return nil, err
		}
		if err = sameRefreshIdentity(identity, j.Entries); err != nil {
			return nil, err
		}
	}
	// Verification must be a real course response, not a 200 home page, login
	// form, identity cookie alone, or an HTTP redirect back into the handshake.
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	b, err := request(ctx, c, http.MethodGet, api+"/mycourse/backclazzdata?view=json&mcode=&rss=1", nil)
	if err != nil {
		return nil, err
	}
	if refreshLoginPage(b) {
		return nil, contract.ErrAuthRequired
	}
	if _, err = parseCourses(b); err != nil {
		return nil, err
	}
	if err = sameRefreshIdentity(identity, j.Entries); err != nil {
		return nil, err
	}
	if !hasIdentity(j) {
		return nil, contract.ErrAuthRequired
	}
	return save(s, j), nil
}

func refreshEntry(u *url.URL) bool {
	if u == nil || u.Scheme != "https" || u.User != nil || (u.Port() != "" && u.Port() != "443") || u.Fragment != "" || u.RawPath != "" {
		return false
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return false
	}
	switch u.Hostname() {
	case "i.chaoxing.com":
		if u.Path != "" && u.Path != "/" && u.Path != "/base" {
			return false
		}
		// The official home navigation may append a millisecond timestamp.
		for key, values := range q {
			if key != "t" || len(values) != 1 || values[0] == "" || len(values[0]) > 20 || strings.Trim(values[0], "0123456789") != "" {
				return false
			}
		}
		return true
	case "passport2.chaoxing.com":
		if u.Path != "/login" {
			return false
		}
		for key, values := range q {
			if len(values) != 1 {
				return false
			}
			switch key {
			case "fid":
				if values[0] != "-1" {
					return false
				}
			case "refer":
				if values[0] != refreshHome && values[0] != refreshHome+"/" && values[0] != refreshHome+"/base" {
					return false
				}
			default:
				return false
			}
		}
		return true
	}
	return false
}

// Check even expired persisted identity evidence: a new server-issued UID may
// not silently replace the account originally bound to this connection.
func refreshIdentity(entries []cookieEntry) (string, error) {
	identity := ""
	for _, entry := range entries {
		c := entry.Cookie
		if (c.Name != "_uid" && c.Name != "UID") || c.MaxAge < 0 || strings.TrimSpace(c.Value) == "" {
			continue
		}
		if identity != "" && identity != c.Value {
			return "", contract.ErrAuthRequired
		}
		identity = c.Value
	}
	return identity, nil
}

func sameRefreshIdentity(before string, entries []cookieEntry) error {
	after, err := refreshIdentity(entries)
	if err != nil || (before != "" && before != after) {
		return contract.ErrAuthRequired
	}
	return nil
}

func refreshLoginPage(b []byte) bool {
	if errors.Is(checked(b), contract.ErrAuthRequired) {
		return true
	}
	d, err := html.Parse(strings.NewReader(string(b)))
	if err != nil {
		return false
	}
	loginPage := false
	walk(d, func(n *html.Node) {
		if n.Data == "input" && strings.EqualFold(attr(n, "type"), "password") {
			loginPage = true
		}
	})
	return loginPage
}
