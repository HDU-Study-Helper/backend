package runcontrol

import (
	"encoding/json"
	"strings"
)

// normalizedResultJSON only removes one complete code fence or appends missing
// closing delimiters at EOF outside strings. It never edits any key or value.
func normalizedResultJSON(raw string) string {
	text := strings.TrimSpace(raw)
	if strings.HasPrefix(text, "```json\n") && strings.HasSuffix(text, "\n```") {
		text = strings.TrimSuffix(strings.TrimPrefix(text, "```json\n"), "\n```")
	} else if strings.HasPrefix(text, "```\n") && strings.HasSuffix(text, "\n```") {
		text = strings.TrimSuffix(strings.TrimPrefix(text, "```\n"), "\n```")
	}
	if json.Valid([]byte(text)) {
		return text
	}
	stack := []byte{}
	quoted, escaped := false, false
	for i := 0; i < len(text); i++ {
		c := text[i]
		if quoted {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				quoted = false
			}
			continue
		}
		switch c {
		case '"':
			quoted = true
		case '{', '[':
			stack = append(stack, c)
		case '}', ']':
			if len(stack) == 0 {
				return raw
			}
			open := stack[len(stack)-1]
			if (open == '{' && c != '}') || (open == '[' && c != ']') {
				return raw
			}
			stack = stack[:len(stack)-1]
		}
	}
	if quoted || len(stack) == 0 {
		return raw
	}
	fixed := text
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i] == '{' {
			fixed += "}"
		} else {
			fixed += "]"
		}
	}
	if json.Valid([]byte(fixed)) {
		return fixed
	}
	return raw
}
