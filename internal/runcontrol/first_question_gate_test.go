package runcontrol

import (
	f "academic/agent/framework"
	"academic/backend/internal/model"
	"context"
	"errors"
	"testing"
)

func TestFirstCourseGateWhileQuestionPending(t *testing.T) {
	ctx := context.Background()
	s, _, store, _, batch := newItemsIntegration(t, "learner_no_change")
	course := model.FirstCourse{Course: model.FirstEvidence{Ref: "math"}}
	item := model.ControlItem{ID: stableItemID(batch.ID, "math"), BatchID: batch.ID, OwnerID: batch.OwnerID, Resource: "selected_course", Status: "NEEDS_USER_INPUT", PendingQuestions: []string{"q"}}
	for _, tc := range []struct {
		name, status string
		verified     bool
		err          error
		ready        bool
	}{{"no known work", "NEEDS_INFO", false, nil, false}, {"unverified", "APPLIED", false, nil, false}, {"failed audit", "APPLIED", true, errors.New("audit failed"), false}, {"known work committed", "APPLIED", true, nil, true}} {
		t.Run(tc.name, func(t *testing.T) {
			err := saveWaitingFirstCourse(ctx, store, item, tc.status, "known work", tc.verified, tc.err)
			if (err == nil) != tc.ready {
				t.Fatalf("wrong gate: %v", err)
			}
			if len(store.items[item.ID].PendingQuestions) != 1 {
				t.Fatal("dropped question")
			}
			err = s.processFirstCourse(ctx, batch, store, "nonexistent", model.FirstPacket{}, course)
			if (err == nil) != tc.ready || err != nil && f.SafeError(err).Code != "FIRST_COURSES_INCOMPLETE" {
				t.Fatalf("retry did not preserve gate: %v", err)
			}
		})
	}
	item.Status = "IGNORED"
	item.PendingQuestions = nil
	if e := store.SaveItem(ctx, item); e != nil {
		t.Fatal(e)
	}
	if e := s.processFirstCourse(ctx, batch, store, "nonexistent", model.FirstPacket{}, course); e != nil {
		t.Fatal("validated answer should let a retry finish without rerun", e)
	}
}
