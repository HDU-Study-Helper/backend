package model

import (
	"encoding/json"
	"testing"
)

func TestConcreteWorkflowEntity(t *testing.T) {
	for _, raw := range []string{`{}`, `{"entity":{"kind":"course","id":"course","name":"课程"}}`, `{"entity":{"kind":"task","id":"x","name":"课程目录"}}`, `{"entity":{"kind":"exam","id":"x","name":"高数考试"}}`} {
		if ValidWorkflowEntity(json.RawMessage(raw)) {
			t.Fatal("generic entity accepted", raw)
		}
	}
	if !ValidWorkflowEntity(json.RawMessage(`{"entity":{"kind":"course","id":"chaoxing/connection/course123/class456","name":"工科数学分析（1）"}}`)) {
		t.Fatal("concrete course rejected")
	}
}
