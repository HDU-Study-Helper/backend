package runcontrol

import (
	"encoding/json"
	"strings"
)

// Models may quote a tool error instead of copying its object. Accept both
// representations without treating a failed dispatch as a successful child run.
type routeError struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

func (e *routeError) UnmarshalJSON(raw []byte) error {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		e.Message = text
		e.Code = "TOOL_ERROR"
		for _, separator := range []string{":", "："} {
			if prefix, _, ok := strings.Cut(text, separator); ok && prefix != "" {
				e.Code = strings.TrimSpace(prefix)
				break
			}
		}
		return nil
	}
	type plain routeError
	return json.Unmarshal(raw, (*plain)(e))
}
