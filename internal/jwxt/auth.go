package jwxt

import (
	"bytes"
	"context"
	"crypto/aes"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"time"

	"academic/backend/internal/contract"
	"golang.org/x/net/html"
)

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
func walk(n *html.Node, fn func(*html.Node)) {
	fn(n)
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		walk(ch, fn)
	}
}
func nodeText(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	if n.Data == "script" || n.Data == "style" {
		return ""
	}
	var s strings.Builder
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		s.WriteString(nodeText(ch))
	}
	return s.String()
}
func loginPage(b []byte) bool {
	s := string(b)
	for _, marker := range []string{"login-page-flowkey", "login_slogin.html", "用户登录", "请先登录"} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}
func encryptedPassword(password, keyText string) (string, error) {
	key, e := base64.StdEncoding.Strict().DecodeString(keyText)
	if e != nil {
		return "", contract.ErrParse
	}
	defer clear(key)
	block, e := aes.NewCipher(key)
	if e != nil {
		return "", contract.ErrParse
	}
	p := []byte(password)
	padding := aes.BlockSize - len(p)%aes.BlockSize
	p = append(p, bytes.Repeat([]byte{byte(padding)}, padding)...)
	defer clear(p)
	result := make([]byte, len(p))
	defer clear(result)
	for i := 0; i < len(p); i += aes.BlockSize {
		block.Encrypt(result[i:i+aes.BlockSize], p[i:i+aes.BlockSize])
	}
	return base64.StdEncoding.EncodeToString(result), nil
}
func (a *adapter) Login(ctx context.Context, scope contract.Scope, username, password string) (contract.AuthResult, error) {
	if strings.TrimSpace(username) == "" || len(username) > 128 || password == "" || len(password) > 1024 {
		return contract.AuthResult{}, contract.ErrInvalid
	}
	c, j, s, e := a.clientFor(scope, nil)
	if e != nil {
		return contract.AuthResult{}, e
	}
	b, u, e := request(ctx, c, http.MethodGet, loginURL, nil, true)
	if e != nil {
		return contract.AuthResult{}, e
	}
	if u.Hostname() != "sso.hdu.edu.cn" || u.Path != "/login" || u.Query().Get("service") != "http://newjw.hdu.edu.cn/sso/driot4login" {
		return contract.AuthResult{}, contract.ErrParse
	}
	d, e := html.Parse(bytes.NewReader(b))
	if e != nil {
		return contract.AuthResult{}, contract.ErrParse
	}
	fields := map[string]string{}
	walk(d, func(n *html.Node) {
		id := attr(n, "id")
		if id == "login-page-flowkey" || id == "login-croypto" {
			value := attr(n, "value")
			if value == "" {
				value = nodeText(n)
			}
			fields[id] = strings.TrimSpace(value)
		}
	})
	if fields["login-page-flowkey"] == "" || fields["login-croypto"] == "" {
		return contract.AuthResult{}, contract.ErrParse
	}
	pass, e := encryptedPassword(password, fields["login-croypto"])
	if e != nil {
		return contract.AuthResult{}, e
	}
	form := url.Values{"username": {username}, "password": {pass}, "execution": {fields["login-page-flowkey"]}, "croypto": {fields["login-croypto"]}, "type": {"UsernamePassword"}, "_eventId": {"submit"}, "geolocation": {""}, "captcha_code": {""}}
	// Exactly one credential POST. No retries or fallback to local RSA login.
	b, u, e = request(ctx, c, http.MethodPost, u.String(), form, true)
	if e != nil {
		return contract.AuthResult{}, e
	}
	if u.Hostname() != "newjw.hdu.edu.cn" || loginPage(b) {
		return contract.AuthResult{}, contract.ErrAuthRequired
	}
	b, u, e = request(ctx, c, http.MethodGet, menu, nil, true)
	if e != nil {
		return contract.AuthResult{}, e
	}
	if u.Hostname() != "newjw.hdu.edu.cn" || u.Path != "/jwglxt/xtgl/index_initMenu.html" || loginPage(b) || !strings.Contains(string(b), "杭州电子科技大学本科教学管理服务平台") {
		return contract.AuthResult{}, contract.ErrAuthRequired
	}
	menuURL, _ := url.Parse(menu)
	hasSession := false
	for _, cookie := range j.Cookies(menuURL) {
		if cookie.Name == "JSESSIONID" && cookie.Value != "" {
			hasSession = true
		}
	}
	if !hasSession {
		return contract.AuthResult{}, contract.ErrAuthRequired
	}
	s.Username = username
	// ExpiresAt is local auth metadata, not a claim about the upstream timeout.
	return contract.AuthResult{Status: "authorized", Session: saveSession(s, j), ExpiresAt: time.Now().Add(8 * time.Hour)}, nil
}
