package mooc

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"time"

	"academic/backend/internal/contract"
)

func (a *adapter) BeginAuth(ctx context.Context, scope contract.Scope) (contract.AuthResult, error) {
	s, c, e := a.restore(scope, nil)
	if e != nil {
		return contract.AuthResult{}, e
	}
	b, e := request(ctx, c, "GET", "/logonByQRCode/code.do?width=182&height=182", origin+"/", nil)
	if e != nil {
		return contract.AuthResult{}, e
	}
	r, e := decode(b)
	if e != nil {
		return contract.AuthResult{}, e
	}
	q, e := url.Parse(text(r["codeUrl"]))
	if e != nil {
		return contract.AuthResult{}, contract.ErrParse
	}
	// Official API currently returns an HTTP image URL. Upgrade to HTTPS on the
	// exact official image host; do not fabricate QR contents or expose pollKey.
	if q.Host != "mooc-image.nosdn.127.net" || (q.Scheme != "http" && q.Scheme != "https") || q.User != nil {
		return contract.AuthResult{}, contract.ErrParse
	}
	q.Scheme = "https"
	s.PollKey = text(r["pollKey"])
	if s.PollKey == "" || len(s.PollKey) > 4096 {
		return contract.AuthResult{}, contract.ErrParse
	}
	s.QRURL = q.String()
	s.Expires = time.Now().UTC().Add(2 * time.Minute)
	imageBytes, media, e := a.downloadQR(ctx, q)
	if e != nil {
		return contract.AuthResult{}, e
	}
	result, e := authResult(s, c, "pending")
	if e != nil {
		return contract.AuthResult{}, e
	}
	result.QRImage = imageBytes
	result.QRMediaType = media
	return result, nil
}

// The image host is a separate credential boundary. Preserve only the injected
// transport; never attach the platform jar, authentication headers or redirects.
func (a *adapter) downloadQR(ctx context.Context, u *url.URL) ([]byte, string, error) {
	if u.Scheme != "https" || u.Host != "mooc-image.nosdn.127.net" || u.User != nil {
		return nil, "", contract.ErrParse
	}
	if e := ctx.Err(); e != nil {
		return nil, "", e
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	client := a.client
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if e != nil {
		return nil, "", contract.ErrParse
	}
	res, e := client.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		return nil, "", contract.ErrUpstream
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusTooManyRequests {
		return nil, "", contract.ErrRateLimited
	}
	if res.StatusCode != http.StatusOK {
		return nil, "", contract.ErrUpstream
	}
	const maxQR = 512 << 10
	b, e := io.ReadAll(io.LimitReader(res.Body, maxQR+1))
	if e != nil {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		return nil, "", contract.ErrUpstream
	}
	if len(b) > maxQR {
		return nil, "", contract.ErrParse
	}
	config, format, e := image.DecodeConfig(bytes.NewReader(b))
	if e != nil || (format != "png" && format != "jpeg") || config.Width < 1 || config.Height < 1 || config.Width > 2048 || config.Height > 2048 {
		return nil, "", contract.ErrParse
	}
	if _, _, e = image.Decode(bytes.NewReader(b)); e != nil {
		return nil, "", contract.ErrParse
	}
	media := "image/png"
	if format == "jpeg" {
		media = "image/jpeg"
	}
	return b, media, nil
}
func authResult(s *state, c *http.Client, status string) (contract.AuthResult, error) {
	session, e := save(s, c)
	if e != nil {
		return contract.AuthResult{}, e
	}
	r := contract.AuthResult{Status: status, ExpiresAt: s.Expires, PollAfter: 2 * time.Second, Session: session}
	if status == "pending" || status == "scanned" {
		r.QRURL = s.QRURL
	}
	return r, nil
}
func (a *adapter) PollAuth(ctx context.Context, scope contract.Scope, session contract.Session) (contract.AuthResult, error) {
	if e := ctx.Err(); e != nil {
		return contract.AuthResult{}, e
	}
	s, c, e := a.restore(scope, session)
	if e != nil {
		return contract.AuthResult{}, e
	}
	if s.Authorized {
		return authResult(s, c, "authorized")
	}
	if s.PollKey == "" {
		return contract.AuthResult{}, contract.ErrInvalid
	}
	if !time.Now().Before(s.Expires) {
		return authResult(s, c, "expired")
	}
	b, e := request(ctx, c, "GET", "/logonByQRCode/poll.do?pollKey="+url.QueryEscape(s.PollKey), origin+"/", nil)
	if e != nil {
		return contract.AuthResult{}, e
	}
	r, e := decode(b)
	if e != nil {
		return contract.AuthResult{}, e
	}
	switch text(r["codeStatus"]) {
	case "0", "4":
		return authResult(s, c, "pending")
	case "1":
		return authResult(s, c, "scanned")
	case "3":
		s.Expires = time.Now().UTC()
		return authResult(s, c, "expired")
	case "2":
		token := text(r["token"])
		if token == "" || len(token) > 8192 {
			return contract.AuthResult{}, contract.ErrParse
		}
		ret := base64.StdEncoding.EncodeToString([]byte(origin + "/"))
		_, e = request(ctx, c, "GET", "/passport/logingate/mocMobChangeCookie.htm?"+url.Values{"token": {token}, "returnUrl": {ret}}.Encode(), origin+"/", nil)
		if e != nil {
			return contract.AuthResult{}, e
		}
		u, _ := url.Parse(origin)
		study, csrf := false, false
		for _, cookie := range c.Jar.Cookies(u) {
			if cookie.Value != "" {
				study = study || cookie.Name == "STUDY_INFO"
				csrf = csrf || cookie.Name == "NTESSTUDYSI"
			}
		}
		if !study || !csrf {
			return contract.AuthResult{}, contract.ErrAuthRequired
		}
		s.Authorized = true
		s.PollKey = ""
		s.QRURL = ""
		return authResult(s, c, "authorized")
	default:
		return contract.AuthResult{}, contract.ErrParse
	}
}
