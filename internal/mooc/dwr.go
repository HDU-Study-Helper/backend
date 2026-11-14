package mooc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"academic/backend/internal/contract"
)

type dwrShapeError struct{ shape map[string]any }

func (e *dwrShapeError) Error() string { return contract.ErrParse.Error() }
func (e *dwrShapeError) Unwrap() error { return contract.ErrParse }
func dwrFailureShape(body string) map[string]any {
	out := map[string]any{"bytes": len(body)}
	statements, e := dwrStatements(body)
	out["split_ok"] = e == nil
	out["statements"] = len(statements)
	shapes := []string{}
	for _, s := range statements {
		if len(shapes) >= 8 {
			break
		}
		s = regexp.MustCompile(`"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'`).ReplaceAllString(s, "[string]")
		s = regexp.MustCompile(`[0-9]+`).ReplaceAllString(s, "N")
		if len(s) > 200 {
			s = s[:200]
		}
		shapes = append(shapes, s)
	}
	out["statement_shapes"] = shapes
	return out
}

type dwrReference string

var dwrVar = regexp.MustCompile(`^s[0-9]{1,5}$`)
var dwrAssignment = regexp.MustCompile(`^(s[0-9]{1,5})(?:\.([A-Za-z_][A-Za-z0-9_]*)|\[([0-9]{1,5})\])$`)

// parseDWR handles a strict data-only subset, never evals JavaScript. Unknown
// executable statements, duplicate variables and dangling references fail closed.
func parseDWR(body string) (any, error) {
	if len(body) > maxBody {
		return nil, contract.ErrParse
	}
	values := map[string]any{}
	returned := false
	var result any
	statements, e := dwrStatements(body)
	if e != nil {
		return nil, e
	}
	if len(statements) > 20000 {
		return nil, contract.ErrParse
	}
	var literal func(string) (any, error)
	literalCalls := 0
	literalDepth := 0
	constructed := 0
	reserve := func(n int) bool {
		if n < 0 || n > 10000-constructed {
			return false
		}
		constructed += n
		return true
	}
	literal = func(s string) (any, error) {
		literalCalls++
		literalDepth++
		defer func() { literalDepth-- }()
		if literalCalls > 20000 || literalDepth > 24 {
			return nil, contract.ErrParse
		}
		s = strings.TrimSpace(s)
		if _, ok := values[s]; ok {
			return dwrReference(s), nil
		}
		switch s {
		case "null":
			return nil, nil
		case "true":
			return true, nil
		case "false":
			return false, nil
		case "{}":
			if !reserve(1) {
				return nil, contract.ErrParse
			}
			return map[string]any{}, nil
		case "[]":
			if !reserve(1) {
				return nil, contract.ErrParse
			}
			return []any{}, nil
		}
		if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
			parts, e := dwrSplit(s[1:len(s)-1], ',')
			if e != nil {
				return nil, e
			}
			if !reserve(1 + len(parts)) {
				return nil, contract.ErrParse
			}
			out := map[string]any{}
			for _, p := range parts {
				colon := strings.IndexByte(p, ':')
				if colon < 0 {
					return nil, contract.ErrParse
				}
				name := strings.TrimSpace(p[:colon])
				if len(name) > 1 && (name[0] == '\'' || name[0] == '"') {
					name, e = dwrString(name)
					if e != nil {
						return nil, e
					}
				}
				if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(name) {
					return nil, contract.ErrParse
				}
				v, e := literal(p[colon+1:])
				if e != nil {
					return nil, e
				}
				out[name] = v
			}
			return out, nil
		}
		if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
			parts, e := dwrSplit(s[1:len(s)-1], ',')
			if e != nil {
				return nil, e
			}
			if !reserve(1 + len(parts)) {
				return nil, contract.ErrParse
			}
			out := []any{}
			for _, p := range parts {
				if strings.TrimSpace(p) == "" {
					continue
				}
				v, e := literal(p)
				if e != nil {
					return nil, e
				}
				out = append(out, v)
			}
			return out, nil
		}
		if m := regexp.MustCompile(`^new Date\((-?[0-9]{1,16})\)$`).FindStringSubmatch(s); m != nil {
			return json.Number(m[1]), nil
		}
		if len(s) > 1 && (s[0] == '\'' || s[0] == '"') {
			return dwrString(s)
		}
		if n, e := strconv.ParseFloat(s, 64); e == nil && n == n && s != "Infinity" && s != "-Infinity" {
			return json.Number(s), nil
		}
		return nil, contract.ErrParse
	}
	for _, statement := range statements {
		s := strings.TrimSpace(statement)
		if s == "" {
			continue
		}
		if s == "throw 'allowScriptTagRemoting is false.'" || s == `throw "allowScriptTagRemoting is false."` {
			continue
		}
		if strings.HasPrefix(s, "dwr.engine._remoteHandleException(") {
			return nil, contract.ErrUpstream
		}
		if strings.HasPrefix(s, "dwr.engine._remoteHandleCallback(") && strings.HasSuffix(s, ")") {
			if returned {
				return nil, contract.ErrParse
			}
			args, e := dwrSplit(s[len("dwr.engine._remoteHandleCallback("):len(s)-1], ',')
			if e != nil || len(args) != 3 {
				return nil, contract.ErrParse
			}
			result, e = literal(args[2])
			if e != nil {
				return nil, e
			}
			returned = true
			continue
		}
		if returned {
			return nil, contract.ErrParse
		}
		if strings.HasPrefix(s, "var ") {
			parts, e := dwrSplit(strings.TrimPrefix(s, "var "), ',')
			if e != nil {
				return nil, e
			}
			for _, p := range parts {
				eq := strings.IndexByte(p, '=')
				if eq < 0 {
					return nil, contract.ErrParse
				}
				name := strings.TrimSpace(p[:eq])
				if !dwrVar.MatchString(name) {
					return nil, contract.ErrParse
				}
				if _, ok := values[name]; ok {
					return nil, contract.ErrParse
				}
				v, e := literal(p[eq+1:])
				if e != nil {
					return nil, e
				}
				values[name] = v
			}
			continue
		}
		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			return nil, contract.ErrParse
		}
		m := dwrAssignment.FindStringSubmatch(strings.TrimSpace(s[:eq]))
		if m == nil {
			return nil, contract.ErrParse
		}
		v, e := literal(s[eq+1:])
		if e != nil {
			return nil, e
		}
		target, ok := values[m[1]]
		if !ok {
			return nil, contract.ErrParse
		}
		if m[2] != "" {
			o, ok := target.(map[string]any)
			if !ok {
				return nil, contract.ErrParse
			}
			if _, exists := o[m[2]]; !exists && !reserve(1) {
				return nil, contract.ErrParse
			}
			o[m[2]] = v
		} else {
			arr, ok := target.([]any)
			if !ok {
				return nil, contract.ErrParse
			}
			i, _ := strconv.Atoi(m[3])
			if i > 2000 {
				return nil, contract.ErrParse
			}
			if growth := i + 1 - len(arr); growth > 0 {
				if !reserve(growth) {
					return nil, contract.ErrParse
				}
				arr = append(arr, make([]any, growth)...)
			}
			arr[i] = v
			values[m[1]] = arr
		}
	}
	if !returned {
		return nil, contract.ErrParse
	}
	visited := 0
	active := map[string]bool{}
	var resolve func(any, int) (any, error)
	resolve = func(v any, depth int) (any, error) {
		visited++
		if depth > 24 || visited > 10000 {
			return nil, contract.ErrParse
		}
		if ref, ok := v.(dwrReference); ok {
			name := string(ref)
			if active[name] {
				return nil, contract.ErrParse
			}
			value, ok := values[name]
			if !ok {
				return nil, contract.ErrParse
			}
			active[name] = true
			out, e := resolve(value, depth+1)
			delete(active, name)
			return out, e
		}
		if obj, ok := v.(map[string]any); ok {
			out := map[string]any{}
			for k, x := range obj {
				value, e := resolve(x, depth+1)
				if e != nil {
					return nil, e
				}
				out[k] = value
			}
			return out, nil
		}
		if arr, ok := v.([]any); ok {
			out := make([]any, len(arr))
			for i, x := range arr {
				value, e := resolve(x, depth+1)
				if e != nil {
					return nil, e
				}
				out[i] = value
			}
			return out, nil
		}
		return v, nil
	}
	return resolve(result, 0)
}
func dwrStatements(body string) ([]string, error) {
	// Remove line comments outside strings only.
	var b strings.Builder
	quote := byte(0)
	escaped := false
	for i := 0; i < len(body); i++ {
		c := body[i]
		if quote != 0 {
			b.WriteByte(c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == quote {
				quote = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			b.WriteByte(c)
			continue
		}
		if c == '/' && i+1 < len(body) && body[i+1] == '/' {
			for i < len(body) && body[i] != '\n' {
				i++
			}
			b.WriteByte('\n')
			continue
		}
		b.WriteByte(c)
	}
	return dwrSplit(b.String(), ';')
}
func dwrSplit(s string, sep byte) ([]string, error) {
	out := []string{}
	quote := byte(0)
	escaped := false
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == quote {
				quote = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			continue
		}
		if c == '[' || c == '{' || c == '(' {
			depth++
			if depth > 24 {
				return nil, contract.ErrParse
			}
		}
		if c == ']' || c == '}' || c == ')' {
			depth--
			if depth < 0 {
				return nil, contract.ErrParse
			}
		}
		if c == sep && depth == 0 {
			if len(out) >= 20000 {
				return nil, contract.ErrParse
			}
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if depth != 0 || quote != 0 {
		return nil, contract.ErrParse
	}
	return append(out, s[start:]), nil
}
func dwrString(s string) (string, error) {
	if len(s) < 2 || s[len(s)-1] != s[0] {
		return "", contract.ErrParse
	}
	if s[0] == '"' {
		var out string
		if json.Unmarshal([]byte(s), &out) != nil {
			return dwrQuotedString(s)
		}
		return out, nil
	}
	return dwrQuotedString(s)
}
func dwrQuotedString(s string) (string, error) {
	var b strings.Builder
	for i := 1; i < len(s)-1; i++ {
		c := s[i]
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		i++
		if i >= len(s)-1 {
			return "", contract.ErrParse
		}
		switch s[i] {
		case '\\', '\'', '"', '/':
			b.WriteByte(s[i])
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'x':
			if i+2 >= len(s) {
				return "", contract.ErrParse
			}
			n, e := strconv.ParseUint(s[i+1:i+3], 16, 8)
			if e != nil {
				return "", contract.ErrParse
			}
			b.WriteByte(byte(n))
			i += 2
		case 'u':
			if i+4 >= len(s) {
				return "", contract.ErrParse
			}
			n, e := strconv.ParseUint(s[i+1:i+5], 16, 16)
			if e != nil {
				return "", contract.ErrParse
			}
			i += 4
			r := rune(n)
			if utf16.IsSurrogate(r) {
				if i+6 >= len(s) || s[i+1:i+3] != `\u` {
					return "", contract.ErrParse
				}
				n2, e := strconv.ParseUint(s[i+3:i+7], 16, 16)
				if e != nil {
					return "", contract.ErrParse
				}
				r = utf16.DecodeRune(r, rune(n2))
				if r == '\uFFFD' {
					return "", contract.ErrParse
				}
				i += 6
			}
			b.WriteRune(r)
		default:
			return "", contract.ErrParse
		}
	}
	return b.String(), nil
}
func readAnnouncements(ctx context.Context, h *http.Client, co course) (any, error) {
	return readDWR(ctx, h, co, "getAllAnnouncementByTerm", []string{sourceTerm(co), "1"})
}
func readUnitBody(ctx context.Context, h *http.Client, co course, content, typ, unit string) (any, error) {
	if !validID(content) || !validID(unit) || (typ != "3" && typ != "4" && typ != "6") {
		return nil, contract.ErrUnsupported
	}
	return readDWR(ctx, h, co, "getLessonUnitLearnVo", []string{content, typ, "0", unit})
}
func readDWR(ctx context.Context, h *http.Client, co course, method string, params []string) (any, error) {
	if method != "getAllAnnouncementByTerm" && method != "getLessonUnitLearnVo" {
		return nil, contract.ErrUnsupported
	}
	return readDWRBean(ctx, h, co, "CourseBean", method, params)
}

// readPostDWR is intentionally limited to client-verified read operations. The
// official viewer invokes browse/unread mutations separately; those are excluded.
func readPostDWR(ctx context.Context, h *http.Client, co course, method string, params []string) (any, error) {
	if method != "getPostDetailById" && method != "getPaginationReplys" {
		return nil, contract.ErrUnsupported
	}
	if method == "getPostDetailById" && len(params) != 1 {
		return nil, contract.ErrInvalid
	}
	if method == "getPaginationReplys" {
		if len(params) != 3 || params[1] != "1" {
			return nil, contract.ErrInvalid
		}
		page, e := strconv.Atoi(params[2])
		if e != nil || page < 1 || page > maxReplyPages {
			return nil, contract.ErrInvalid
		}
	}
	return readDWRBean(ctx, h, co, "PostBean", method, params)
}
func readDWRBean(ctx context.Context, h *http.Client, co course, bean, method string, params []string) (any, error) {
	attempts := 1
	if bean == "PostBean" && (method == "getPostDetailById" || method == "getPaginationReplys") {
		attempts = 2
	}
	for attempt := 0; ; attempt++ {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		v, e := readDWRBeanOnce(ctx, h, co, bean, method, params)
		if e == nil {
			return v, nil
		}
		if parentErr := ctx.Err(); parentErr != nil {
			return nil, parentErr
		}
		// A per-attempt deadline may retry while the caller's context is active.
		// Generic upstream errors include remote exceptions and must not retry.
		if attempt+1 == attempts || (!errors.Is(e, errTransientRead) && !errors.Is(e, context.DeadlineExceeded)) {
			if errors.Is(e, errTransientRead) {
				return nil, contract.ErrUpstream
			}
			return nil, e
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// Each attempt rebuilds the request using the current cookie jar and CSRF token.
func readDWRBeanOnce(ctx context.Context, h *http.Client, co course, bean, method string, params []string) (any, error) {
	u, _ := url.Parse(origin)
	session := ""
	for _, c := range h.Jar.Cookies(u) {
		if c.Name == "NTESSTUDYSI" {
			session = c.Value
		}
	}
	if session == "" {
		return nil, contract.ErrAuthRequired
	}
	lines := []string{"callCount=1", "scriptSessionId=${scriptSessionId}190", "httpSessionId=" + session, "c0-scriptName=" + bean, "c0-methodName=" + method, "c0-id=0", "batchId=0"}
	for i, p := range params {
		if !validID(p) {
			return nil, contract.ErrInvalid
		}
		lines = append(lines, "c0-param"+strconv.Itoa(i)+"=number:"+p)
	}
	body := strings.Join(lines, "\n")
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, "POST", origin+"/dwr/call/plaincall/"+bean+"."+method+".dwr", strings.NewReader(body))
	if e != nil {
		return nil, contract.ErrInvalid
	}
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Referer", co.referer())
	req.Header.Set("Origin", origin)
	req.Header.Set("edu-script-token", session)
	res, e := h.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(e, context.Canceled) {
			return nil, context.Canceled
		}
		return nil, errTransientRead
	}
	defer res.Body.Close()
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return nil, contract.ErrAuthRequired
	}
	if res.StatusCode == 429 {
		return nil, contract.ErrRateLimited
	}
	if res.StatusCode == 408 || res.StatusCode == 502 || res.StatusCode == 503 || res.StatusCode == 504 {
		return nil, errTransientRead
	}
	if res.StatusCode != 200 {
		return nil, contract.ErrUpstream
	}
	b, e := io.ReadAll(io.LimitReader(res.Body, maxBody+1))
	if e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(e, context.Canceled) {
			return nil, context.Canceled
		}
		return nil, errTransientRead
	}
	v, e := parseDWR(string(b))
	if e != nil && method == "getLessonUnitLearnVo" {
		return nil, &dwrShapeError{shape: dwrFailureShape(string(b))}
	}
	return v, e
}
