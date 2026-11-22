// Package defaultroles packages the reviewed role definitions with the backend.
package defaultroles

import (
	f "academic/agent/framework"
	"embed"
	"encoding/json"
	"fmt"
)

//go:embed templates/*.json
var files embed.FS

// Definitions are in dispatch dependency order; callers substitute only host IDs.
func Definitions() ([]f.AgentDefinition, error) {
	var defs []f.AgentDefinition
	for _, role := range []string{"trigger", "learner", "worker", "filter", "chat"} {
		raw, e := files.ReadFile("templates/" + role + ".json")
		if e != nil {
			return nil, e
		}
		var d f.AgentDefinition
		if json.Unmarshal(raw, &d) != nil || d.Type != role || d.Name == "" || d.SystemPrompt == "" {
			return nil, fmt.Errorf("invalid default role %s", role)
		}
		d.Model = f.ModelConfig{Mode: "default", Provider: "@configured"}
		d.Limits = d.Limits.Defaults()
		defs = append(defs, d)
	}
	return defs, nil
}
