package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	f "academic/agent/framework"
	"academic/backend/internal/bridge"
	"academic/backend/internal/model"
)

type scheduleContractPort struct {
	calls      int
	operations []string
}

func (p *scheduleContractPort) Call(_ context.Context, r bridge.Request) (bridge.Reply, error) {
	p.calls++
	p.operations = append(p.operations, r.Operation)
	now := time.Date(2026, 11, 17, 8, 0, 0, 0, time.UTC)
	row := model.Schedule{ID: "schedule-one", UserID: r.OwnerID, Semester: model.Semester{AcademicYear: 2026, Term: 1}, ScheduleFields: model.ScheduleFields{TeachingWeek: 8, StartTime: now, EndTime: now.Add(time.Hour), Title: "valid", Details: ""}, Source: "AGENT", TimeStatus: "NOT_STARTED", EventStatus: "INCOMPLETE", Active: true, Mod: model.ScheduleMod{History: []model.ScheduleChange{}}, CreatedAt: now, UpdatedAt: now}
	for _, mode := range []string{"TEMPORARY", "PERMANENT", "SYNC", "DELETE", "SYNC_REMOVED"} {
		row.Mod.History = append(row.Mod.History, model.ScheduleChange{Mode: mode, Before: row.ScheduleFields, After: row.ScheduleFields, EffectiveWeek: 8, OriginalID: row.ID, At: now})
	}
	var data any = row
	if r.Operation == "schedule.query" {
		data = []model.Schedule{row}
	}
	raw, _ := json.Marshal(data)
	return bridge.Reply{Version: bridge.Version, Operation: r.Operation, Payload: raw}, nil
}
func runScheduleContract(t *testing.T, name string, args map[string]any, p *scheduleContractPort) f.ToolResult {
	t.Helper()
	a := testAgent("schedule-check", "owner")
	a.EnabledTools = []string{name}
	a.Permissions = []string{"schedule.read", "schedule.write", "schedule.delete"}
	repo := &repoStub{agents: map[string]f.AgentInstance{a.ID: a}, runs: map[string]f.RunRecord{}}
	round := 0
	var got f.ToolResult
	svc, err := New(Dependencies{Repository: repo, Schedules: p, Providers: map[string]f.ModelProvider{a.ID: fnModel(func(_ context.Context, q f.ModelRequest) (f.ModelResponse, error) {
		round++
		if round == 1 {
			raw, _ := json.Marshal(args)
			return stagedCalls(name, string(raw)), nil
		}
		for _, m := range q.Messages {
			if m.Role == "tool" {
				if err := json.Unmarshal([]byte(m.Content), &got); err != nil {
					t.Fatal(err)
				}
			}
		}
		return done(), nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Run(context.Background(), "owner", f.RunRequest{AgentID: a.ID, Trigger: f.UserMessage, Input: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	return got
}
func createScheduleArgs() map[string]any {
	return map[string]any{"academic_year": 2026, "term": 1, "teaching_week": 8, "start_time": "2026-11-17T16:00:00+08:00", "end_time": "2026-11-17T17:00:00+08:00", "title": "有效标题"}
}
func TestScheduleByteLimitsAndEmptyPatchRejectBeforePort(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		length      int
	}{{"schedule_create", "title", 501}, {"schedule_create", "details", 6000}, {"schedule_update", "title", 501}, {"schedule_update", "details", 6000}} {
		t.Run(tc.name+tc.field, func(t *testing.T) {
			args := createScheduleArgs()
			if tc.name == "schedule_update" {
				args = map[string]any{"id": "schedule-one", "mode": "TEMPORARY"}
			}
			args[tc.field] = strings.Repeat("汉", tc.length)
			p := &scheduleContractPort{}
			got := runScheduleContract(t, tc.name, args, p)
			if got.OK || got.Error == nil || got.Error.Code != "INVALID_ARGUMENT" || !strings.Contains(got.Error.Message, "UTF-8 byte") || p.calls != 0 {
				t.Fatal(got, p.calls)
			}
		})
	}
	p := &scheduleContractPort{}
	got := runScheduleContract(t, "schedule_update", map[string]any{"id": "schedule-one", "mode": "TEMPORARY"}, p)
	if got.OK || got.Error == nil || got.Error.Code != "INVALID_ARGUMENT" || p.calls != 0 {
		t.Fatal(got, p.calls)
	}
	p = &scheduleContractPort{}
	got = runScheduleContract(t, "schedule_create", createScheduleArgs(), p)
	if !got.OK || p.calls != 1 {
		t.Fatal(got, p.calls)
	}
}
func TestScheduleQueryOffsetLimitIsExplicit(t *testing.T) {
	p := &scheduleContractPort{}
	got := runScheduleContract(t, "schedule_query", map[string]any{"from": "2026-11-01T00:00:00+08:00", "to": "2026-12-01T00:00:00+08:00", "offset": 100000}, p)
	if !got.OK {
		t.Fatal(got)
	}
	raw, _ := json.Marshal(got.Data)
	var page map[string]any
	_ = json.Unmarshal(raw, &page)
	if page["page_limit_reason"] != "offset_limit" || page["next_offset"] != float64(100001) {
		t.Fatal(page)
	}
}
func TestScheduleContractsDescribeFieldsAndHistory(t *testing.T) {
	r := f.NewToolRegistry()
	if err := RegisterScheduleTools(r, nil); err != nil {
		t.Fatal(err)
	}
	for _, spec := range r.Catalog() {
		var schema map[string]any
		_ = json.Unmarshal(spec.InputSchema, &schema)
		for k, v := range schema["properties"].(map[string]any) {
			if v.(map[string]any)["description"] == nil {
				t.Fatal(spec.Name, k)
			}
		}
		if spec.Name == "schedule_get" {
			var out map[string]any
			_ = json.Unmarshal(spec.OutputSchema, &out)
			props := out["properties"].(map[string]any)
			mod := props["mod"].(map[string]any)["properties"].(map[string]any)
			if mod["history"] == nil || mod["base"] == nil || props["start_time"].(map[string]any)["format"] != "date-time" {
				t.Fatal(out)
			}
		}
	}
	read := f.NewToolRegistry()
	if err := RegisterScheduleReadTools(read, &scheduleContractPort{}); err != nil {
		t.Fatal(err)
	}
	if len(read.Catalog()) != 2 {
		t.Fatal(read.Catalog())
	}
	for _, s := range read.Catalog() {
		if s.Name != "schedule_query" && s.Name != "schedule_get" {
			t.Fatal(s.Name)
		}
	}
	p := &scheduleContractPort{}
	_, err := (scheduleReadPort{p}).Call(context.Background(), bridge.Request{Operation: "schedule.create"})
	if f.SafeError(err).Code != "PERMISSION_DENIED" || p.calls != 0 {
		t.Fatal(err, p.calls)
	}
}
