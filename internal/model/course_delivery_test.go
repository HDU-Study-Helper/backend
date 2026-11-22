package model

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestDefaultCourseDeliveryMissingTimeAndLocation(t *testing.T) {
	for _, fields := range []string{`{}`, `{"schedule_raw":"--","location_raw":"--"}`, `{"schedule_raw":" — ","location_raw":"-","location":"  "}`, `{"schedule_raw":null,"location_raw":null,"location":null}`} {
		record := PlatformRecord{Source: "jwxt", Kind: "course", Hash: "keep-hash", ObservedAt: time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC), Content: json.RawMessage(`{"title":"fixture","fields":` + fields + `}`)}
		content := append([]byte(nil), record.Content...)
		hash := record.Hash
		at := record.ObservedAt
		got := DefaultCourseDelivery(record)
		if got == nil || got.Mode != "online" || got.Basis != "default_missing_time_and_location" || !got.IsDefault || got.Label != "线上课程（默认）" {
			t.Fatalf("fields=%s got=%+v", fields, got)
		}
		if !bytes.Equal(record.Content, content) || record.Hash != hash || !record.ObservedAt.Equal(at) || record.CourseDelivery != nil {
			t.Fatal("source record mutated")
		}
	}
}
func TestDefaultCourseDeliveryDoesNotGuessOtherData(t *testing.T) {
	for _, tc := range []struct{ name, source, kind, content string }{
		{"other-source", "chaoxing", "course", `{"fields":{}}`}, {"other-resource", "jwxt", "schedule", `{"fields":{}}`},
		{"time-present", "jwxt", "course", `{"fields":{"schedule_raw":"星期一第1-2节","location_raw":"--"}}`},
		{"room-present", "jwxt", "course", `{"fields":{"schedule_raw":"--","location_raw":"101"}}`},
		{"location-alias-present", "jwxt", "course", `{"fields":{"location":"教学楼101"}}`},
		{"practice", "jwxt", "course", `{"fields":{"schedule_raw":"--","location_raw":"课外实践不在教室"}}`},
		{"online-source-text", "jwxt", "course", `{"fields":{"location_raw":"线上"}}`},
		{"pending-text", "jwxt", "course", `{"fields":{"schedule_raw":"待定"}}`},
		{"invalid-number", "jwxt", "course", `{"fields":{"schedule_raw":0}}`},
		{"invalid-object", "jwxt", "course", `{"fields":{"location_raw":{}}}`},
		{"invalid-bool", "jwxt", "course", `{"fields":{"location":false}}`},
		{"invalid-fields", "jwxt", "course", `{"fields":[]}`}, {"missing-fields", "jwxt", "course", `{}`}, {"null-fields", "jwxt", "course", `{"fields":null}`},
		{"malformed", "jwxt", "course", `not json`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DefaultCourseDelivery(PlatformRecord{Source: tc.source, Kind: tc.kind, Content: json.RawMessage(tc.content)}); got != nil {
				t.Fatal(got)
			}
		})
	}
}
func TestCourseDeliverySerializedAsSeparateDefaultMetadata(t *testing.T) {
	r := PlatformRecord{Source: "jwxt", Kind: "course", Content: json.RawMessage(`{"fields":{"schedule_raw":"--","location_raw":"--"}}`)}
	r.CourseDelivery = DefaultCourseDelivery(r)
	raw, e := json.Marshal(r)
	if e != nil {
		t.Fatal(e)
	}
	var obj map[string]json.RawMessage
	_ = json.Unmarshal(raw, &obj)
	if !bytes.Equal(obj["content"], r.Content) {
		t.Fatal("raw fields changed")
	}
	var d CourseDelivery
	if json.Unmarshal(obj["course_delivery"], &d) != nil || !d.IsDefault {
		t.Fatal(string(raw))
	}
	r.Content = json.RawMessage(`{"fields":{"schedule_raw":"星期三第3-4节","location_raw":"--"}}`)
	r.CourseDelivery = DefaultCourseDelivery(r)
	raw, _ = json.Marshal(r)
	if bytes.Contains(raw, []byte(`"course_delivery"`)) {
		t.Fatal("default did not disappear after source time supplied")
	}
}
